package helper

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
)

var errFirstSemanticTimeout = errors.New("first_semantic_timeout")
var errStreamStaging = errors.New("upstream_stream_staging")

const maxStreamStagingBytes = 1 << 20

var streamBufferBytes atomic.Int64

// streamStagingWriter 在真正语义输出出现之前隔离头部和 SSE，失败时可以完整丢弃。
type streamStagingWriter struct {
	gin.ResponseWriter
	mu                       sync.Mutex
	header                   http.Header
	status                   int
	pending                  []byte
	staged                   bytes.Buffer
	state                    atomic.Int32 // 0 等待语义，1 已观察语义，2 超时
	err                      error
	terminal                 bool
	info                     *relaycommon.RelayInfo
	atomicTurn               bool
	holdTerminal             bool
	jsonObserved             bool
	observationOnly          bool
	observationDropped       bool
	bufferLimit, globalLimit int
	accounted                int64
}

func (w *streamStagingWriter) Header() http.Header { return w.header }
func (w *streamStagingWriter) Status() int         { return w.status }
func (w *streamStagingWriter) WriteHeader(code int) {
	if !w.Written() && code > 0 {
		w.status = code
	}
}
func (w *streamStagingWriter) WriteHeaderNow() {}
func (w *streamStagingWriter) Flush() {
	if w.Written() {
		w.ResponseWriter.Flush()
	}
}
func (w *streamStagingWriter) Unwrap() http.ResponseWriter       { return w.ResponseWriter }
func (w *streamStagingWriter) ClientResponseWritten() bool       { return w.ResponseWriter.Written() }
func (w *streamStagingWriter) WriteString(s string) (int, error) { return w.Write([]byte(s)) }

func (w *streamStagingWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.err != nil {
		return 0, w.err
	}
	if w.state.Load() == 2 {
		return 0, errFirstSemanticTimeout
	}
	if w.observationOnly && !strings.HasPrefix(w.header.Get("Content-Type"), "text/event-stream") {
		// Shadow copies only bounded bytes while preserving normal JSON delivery.
		if !w.observationDropped {
			if err := w.accountBuffer(w.staged.Len() + len(p)); err != nil {
				w.observationDropped = true
				w.staged.Reset()
				_ = w.accountBuffer(0)
			} else {
				_, _ = w.staged.Write(p)
			}
		}
		for key, values := range w.header {
			w.ResponseWriter.Header()[key] = values
		}
		w.ResponseWriter.WriteHeader(w.status)
		return w.ResponseWriter.Write(p)
	}
	if err := w.accountBuffer(w.staged.Len() + len(w.pending) + len(p)); err != nil {
		w.err = err
		return 0, err
	}
	defer func() { _ = w.accountBuffer(w.staged.Len() + len(w.pending)) }()
	if !strings.HasPrefix(w.header.Get("Content-Type"), "text/event-stream") {
		if w.staged.Len()+len(p) > w.bufferLimit {
			w.err = types.NewResponseBufferUnavailable()
			return 0, w.err
		}
		_, _ = w.staged.Write(p)
		return len(p), nil
	}
	w.pending = append(w.pending, p...)
	if len(w.pending) > getScannerBufferSize() {
		w.err = types.NewResponseBufferUnavailable()
		return 0, w.err
	}
	for {
		index, delimiter := bytes.Index(w.pending, []byte("\n\n")), 2
		if crlf := bytes.Index(w.pending, []byte("\r\n\r\n")); crlf >= 0 && (index < 0 || crlf < index) {
			index, delimiter = crlf, 4
		}
		if index < 0 {
			break
		}
		frame := append([]byte(nil), w.pending[:index+delimiter]...)
		w.pending = w.pending[index+delimiter:]
		var data []string
		for _, line := range strings.Split(strings.ReplaceAll(string(frame), "\r\n", "\n"), "\n") {
			if value, ok := ParseSSEField(line, "data"); ok {
				data = append(data, value)
			}
		}
		if len(data) == 0 {
			continue
		}
		payload := strings.Join(data, "\n")
		if w.info.Conversation != nil {
			mapped, err := w.info.Conversation.ObserveResponse([]byte(payload), true)
			if err != nil {
				w.err = err
				return 0, err
			}
			if !w.observationOnly {
				// Preserve event/id/retry fields and replace only the data object.
				var lines []string
				for _, line := range strings.Split(strings.ReplaceAll(string(frame), "\r\n", "\n"), "\n") {
					if line != "" && !strings.HasPrefix(line, "data:") {
						lines = append(lines, line)
					}
				}
				lines = append(lines, "data: "+string(mapped))
				frame = []byte(strings.Join(lines, "\n") + "\n\n")
				payload = string(mapped)
			}
		}
		meaning := relaycommon.ClassifyStreamPayload(payload)
		if !meaning.Valid || meaning.Failed {
			w.err = fmt.Errorf("%w: malformed or failed SSE: %s", errStreamStaging, common.LocalLogPreview(payload))
			return 0, w.err
		}
		w.terminal = w.terminal || meaning.Terminal
		if meaning.Semantic || meaning.Terminal && w.info.Conversation != nil {
			if !w.state.CompareAndSwap(0, 1) && w.state.Load() == 2 {
				return 0, errFirstSemanticTimeout
			}
		}
		if !w.Written() || w.atomicTurn || w.holdTerminal && w.terminal {
			if err := w.accountBuffer(w.staged.Len() + len(w.pending) + len(frame)); err != nil {
				w.err = err
				return 0, err
			}
			_, _ = w.staged.Write(frame)
			if meaning.Semantic && !w.atomicTurn && !(w.holdTerminal && w.terminal) {
				if err := w.commit(); err != nil {
					return 0, err
				}
			}
		} else if _, err := w.ResponseWriter.Write(frame); err != nil {
			w.err = err
			return 0, err
		}
	}
	if !w.atomicTurn && w.state.Load() == 0 && !w.Written() && w.staged.Len()+len(w.pending) > maxStreamStagingBytes {
		w.err = types.NewResponseBufferUnavailable()
		return 0, w.err
	}
	return len(p), nil
}

func (w *streamStagingWriter) accountBuffer(size int) error {
	if size > w.bufferLimit {
		return types.NewResponseBufferUnavailable()
	}
	delta := int64(size) - w.accounted
	for {
		used := streamBufferBytes.Load()
		if delta > 0 && used+delta > int64(w.globalLimit) {
			return types.NewResponseBufferUnavailable()
		}
		if streamBufferBytes.CompareAndSwap(used, used+delta) {
			w.accounted += delta
			return nil
		}
	}
}

func (w *streamStagingWriter) commit() error {
	if w.info.Conversation != nil && !w.observationOnly {
		w.header.Del("Content-Length")
		w.ResponseWriter.Header().Del("Content-Length")
	}
	for key, values := range w.header {
		w.ResponseWriter.Header()[key] = append([]string(nil), values...)
	}
	w.ResponseWriter.WriteHeader(w.status)
	written, err := w.ResponseWriter.Write(w.staged.Bytes())
	w.err = err
	if w.err == nil && written != w.staged.Len() {
		w.err = io.ErrShortWrite
	}
	w.staged.Reset()
	_ = w.accountBuffer(len(w.pending))
	if w.Written() && w.info.StreamStatus != nil {
		w.info.StreamStatus.MarkClientCommitted()
	}
	w.info.SetFirstResponseTime()
	return w.err
}

func (w *streamStagingWriter) validate() *types.NewAPIError {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.state.Load() == 2 {
		return types.NewOpenAIError(errFirstSemanticTimeout, types.ErrorCodeChannelResponseTimeExceeded, http.StatusGatewayTimeout)
	}
	if w.err != nil {
		var apiErr *types.NewAPIError
		if errors.As(w.err, &apiErr) && types.IsLocalRelayRejection(apiErr) {
			return apiErr
		}
		return types.NewOpenAIError(w.err, types.ErrorCodeBadResponseBody, http.StatusBadGateway)
	}
	if !strings.HasPrefix(w.header.Get("Content-Type"), "text/event-stream") {
		if w.observationOnly {
			if !w.jsonObserved && !w.observationDropped {
				_, _ = w.info.Conversation.ObserveResponse(w.staged.Bytes(), false)
			}
			w.jsonObserved = true
			w.staged.Reset()
			_ = w.accountBuffer(0)
			return nil
		}
		if w.info.Conversation != nil && !w.jsonObserved {
			mapped, err := w.info.Conversation.ObserveResponse(w.staged.Bytes(), false)
			if err != nil {
				w.err = err
				var apiErr *types.NewAPIError
				if errors.As(err, &apiErr) && types.IsLocalRelayRejection(apiErr) {
					return apiErr
				}
				return types.NewOpenAIError(err, types.ErrorCodeBadResponseBody, http.StatusBadGateway)
			}
			if err := w.accountBuffer(len(mapped)); err != nil {
				w.err = err
				return types.NewResponseBufferUnavailable()
			}
			w.staged.Reset()
			_, _ = w.staged.Write(mapped)
			w.jsonObserved = true
		}
		if w.info.Conversation != nil {
			if err := w.info.Conversation.ValidateResponse(); err != nil {
				return types.NewOpenAIError(err, types.ErrorCodeBadResponseBody, http.StatusBadGateway)
			}
		}
		return nil
	}
	if !w.Written() && w.state.Load() != 1 {
		return types.NewOpenAIError(errors.New("upstream stream ended without semantic output"), types.ErrorCodeEmptyResponse, http.StatusBadGateway)
	}
	if len(bytes.TrimSpace(w.pending)) > 0 || !w.terminal {
		return types.NewOpenAIError(errors.New("upstream stream ended before a protocol terminal"), types.ErrorCodeTruncatedResponse, http.StatusBadGateway)
	}
	if w.info.Conversation != nil {
		if err := w.info.Conversation.ValidateResponse(); err != nil {
			return types.NewOpenAIError(err, types.ErrorCodeBadResponseBody, http.StatusBadGateway)
		}
	}
	return nil
}

func (w *streamStagingWriter) FinalizeResponse() error {
	if err := w.validate(); err != nil {
		return err
	}
	return nil
}

func ValidateStreamStaging(c *gin.Context) *types.NewAPIError {
	if writer, ok := c.Writer.(*streamStagingWriter); ok {
		return writer.validate()
	}
	return nil
}

func WithStreamStaging(c *gin.Context, info *relaycommon.RelayInfo, call func() *types.NewAPIError) *types.NewAPIError {
	if (!info.IsStream && info.Conversation == nil) || info.RelayFormat == types.RelayFormatOpenAIRealtime {
		return call()
	}
	originalWriter, originalRequest := c.Writer, c.Request
	settings := operation_setting.GetConversationRecoverySetting()
	channelSettings, _ := common.GetContextKeyType[dto.ChannelSettings](c, constant.ContextKeyChannelSetting)
	if info.ChannelMeta != nil {
		channelSettings = info.ChannelSetting
	}
	observationOnly := info.Conversation != nil && info.Conversation.ObservationOnly()
	writer := &streamStagingWriter{ResponseWriter: originalWriter, header: originalWriter.Header().Clone(), status: http.StatusOK, info: info, atomicTurn: channelSettings.StreamCommitMode == "atomic_turn", holdTerminal: info.Conversation != nil && !observationOnly, observationOnly: observationOnly, bufferLimit: settings.MaxBufferBytes, globalLimit: settings.GlobalBufferBytes}
	if info.Conversation != nil {
		writer.bufferLimit, writer.globalLimit = info.Conversation.BufferLimits()
	}
	c.Writer = writer
	c.Set("event_stream_headers_set", false)
	ctx, cancel := context.WithCancelCause(originalRequest.Context())
	c.Request = originalRequest.WithContext(ctx)
	if info.FirstSemanticTimeoutSeconds <= 0 {
		info.FirstSemanticTimeoutSeconds = common.GetRelayFirstByteTimeout()
	}
	timeout := time.Duration(info.FirstSemanticTimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	if !info.IsStream {
		writer.state.Store(1)
	}
	timer := time.AfterFunc(timeout, func() {
		if writer.state.CompareAndSwap(0, 2) {
			cancel(errFirstSemanticTimeout)
		}
	})
	defer func() {
		timer.Stop()
		cancel(nil)
		c.Writer, c.Request = originalWriter, originalRequest
		c.Set("event_stream_headers_set", false)
		_ = writer.accountBuffer(0)
	}()
	err := call()
	if writer.state.Load() == 2 {
		if info.StreamStatus == nil {
			info.StreamStatus = relaycommon.NewStreamStatus()
		}
		info.StreamStatus.SetTransportEnd(relaycommon.StreamEndReasonFirstByteTimeout, errFirstSemanticTimeout)
		err = writer.validate()
	}
	if err == nil || writer.err != nil {
		err = writer.validate()
	}
	if err == nil && info.SettlementError != nil {
		if info.SettlementStarted {
			err = types.NewBillingCommitUnavailable()
		} else {
			err = types.NewConversationStoreUnavailable()
		}
	}
	if err == nil && info.Conversation != nil && !info.Conversation.CommitPublished() {
		err = types.NewConversationStoreUnavailable()
	}
	if err == nil && writer.staged.Len() > 0 {
		if commitErr := writer.commit(); commitErr != nil {
			err = types.NewError(commitErr, types.ErrorCodeBadResponse, types.ErrOptionWithSkipRetry())
		}
	}
	return err
}
