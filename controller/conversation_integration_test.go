package controller

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	perfmetrics "github.com/QuantumNous/new-api/pkg/perf_metrics"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func setupManagedIntegration(t *testing.T) {
	t.Helper()
	keys, err := common.Marshal(map[string]string{"test": base64.StdEncoding.EncodeToString(make([]byte, 32))})
	require.NoError(t, err)
	t.Setenv("RENEWAPI_STATE_KEYS", string(keys))
	t.Setenv("RENEWAPI_STATE_ACTIVE_KEY", "test")
	require.NoError(t, model.MigrateConversationState())
	require.NoError(t, model.DB.AutoMigrate(&model.ChannelIdentity{}, &model.ChannelCredentialSlot{}))
}

func TestManagedConversationWSToolRecoveryAfterReconnect(t *testing.T) {
	for _, kind := range []string{"function_call", "custom_tool_call"} {
		t.Run(kind, func(t *testing.T) {
			db := setupEmptyStreamRecoveryDB(t)
			setupManagedIntegration(t)
			var mu sync.Mutex
			var requests [][]byte
			var paths []string
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				mu.Lock()
				requests, paths = append(requests, body), append(paths, r.URL.Path)
				n := len(requests)
				mu.Unlock()
				if n == 1 {
					call := map[string]any{"type": kind, "call_id": "call_once", "id": "tool_item", "name": "lookup", "namespace": "functions", "status": "completed"}
					if kind == "function_call" {
						call["arguments"] = `{"q":"test"}`
					} else {
						call["input"] = "test"
					}
					response := map[string]any{"id": "resp_tool_a", "status": "completed", "output": []any{call}, "usage": map[string]int{"input_tokens": 10, "output_tokens": 5, "total_tokens": 15}}
					event, _ := common.Marshal(map[string]any{"type": "response.completed", "response": response})
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = fmt.Fprintf(w, "data: %s\n\n", event)
					return
				}
				if strings.HasPrefix(r.URL.Path, "/a/") {
					w.WriteHeader(429)
					_, _ = io.WriteString(w, `{"error":{"type":"rate_limit_error","message":"busy"}}`)
					return
				}
				managedSuccess(w, fmt.Sprintf("resp_b_%d", n), "done", true)
			}))
			t.Cleanup(upstream.Close)
			for i, name := range []string{"a", "b"} {
				ch := recoveryIntegrationChannel(8851+i, name, upstream.URL+"/"+name, int64(20-i), failoverTestModel)
				ch.Setting = common.GetPointer(`{"conversation_recovery":"managed","client_identity_mode":"per_channel","stream_commit_mode":"atomic_turn"}`)
				require.NoError(t, db.Create(ch).Error)
				require.NoError(t, ch.AddAbilities(nil))
			}
			endpoint := responsesWSTestGateway(t, db)
			conn := dialResponsesWSTest(t, endpoint)
			sendResponsesWSTest(t, conn, map[string]any{"type": "response.create", "model": failoverTestModel, "input": "use the tool"})
			_, first := readResponsesWSTerminal(t, conn)
			require.Equal(t, "response.completed", first.Get("type").String(), first.Raw)
			id := first.Get("response.id").String()
			require.True(t, strings.HasPrefix(id, service.ManagedResponsePrefix))
			require.NoError(t, conn.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, "")))
			_ = conn.Close()
			conn = dialResponsesWSTest(t, endpoint)
			sendResponsesWSTest(t, conn, map[string]any{"type": "response.create", "model": failoverTestModel, "previous_response_id": id, "input": []any{map[string]any{"type": kind + "_output", "call_id": "call_once", "output": "tool result"}}})
			events, second := readResponsesWSTerminal(t, conn)
			require.Equal(t, "response.completed", second.Get("type").String(), second.Raw)
			for _, event := range events {
				require.NotContains(t, event.Raw, `"type":"`+kind+`"`, "completed client tool must not run twice")
			}
			sendResponsesWSTest(t, conn, map[string]any{"type": "response.create", "model": failoverTestModel, "previous_response_id": second.Get("response.id").String(), "input": "next"})
			_, third := readResponsesWSTerminal(t, conn)
			require.Equal(t, "response.completed", third.Get("type").String(), third.Raw)
			mu.Lock()
			defer mu.Unlock()
			require.Equal(t, []string{"/a/v1/responses", "/a/v1/responses", "/b/v1/responses", "/b/v1/responses"}, paths)
			require.Equal(t, kind, gjson.GetBytes(requests[2], "input.1.type").String())
			require.Equal(t, "functions", gjson.GetBytes(requests[2], "input.1.namespace").String())
			require.Equal(t, "call_once", gjson.GetBytes(requests[2], "input.2.call_id").String())
			require.EqualValues(t, 3, gjson.GetBytes(requests[2], "input.#").Int())
		})
	}
}

func managedIntegrationRouter() *gin.Engine {
	router := gin.New()
	router.Use(middleware.BodyStorageCleanup(), func(c *gin.Context) {
		for _, key := range []constant.ContextKey{constant.ContextKeyUserId, constant.ContextKeyTokenId} {
			common.SetContextKey(c, key, 1)
		}
		for _, key := range []constant.ContextKey{constant.ContextKeyUserGroup, constant.ContextKeyUsingGroup, constant.ContextKeyTokenGroup} {
			common.SetContextKey(c, key, "default")
		}
		common.SetContextKey(c, constant.ContextKeyTokenKey, "sk-recovery-test")
		common.SetContextKey(c, constant.ContextKeyUserSetting, dto.UserSetting{BillingPreference: "wallet_only"})
		common.SetContextKey(c, common.RequestIdKey, uuid.NewString())
		c.Next()
	})
	router.POST("/v1/responses", middleware.ConversationContext(), middleware.Distribute(), func(c *gin.Context) { Relay(c, types.RelayFormatOpenAIResponses) })
	return router
}

func managedSuccess(w http.ResponseWriter, id, text string, stream bool) {
	response := fmt.Sprintf(`{"id":%q,"object":"response","status":"completed","model":"recovery-integration-model","output":[{"type":"message","id":"msg_1","role":"assistant","status":"completed","content":[{"type":"output_text","text":%q}]}],"usage":{"input_tokens":11,"output_tokens":3,"total_tokens":14}}`, id, text)
	if stream {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprintf(w, "data: {\"type\":\"response.created\",\"response\":{\"id\":%q,\"status\":\"in_progress\"}}\n\ndata: {\"type\":\"response.output_text.delta\",\"delta\":%q}\n\ndata: {\"type\":\"response.completed\",\"response\":%s}\n\n", id, text, response)
	} else {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, response)
	}
}

func TestManagedConversationHTTPFailoverAndStickyContinuation(t *testing.T) {
	for _, mode := range []string{"json", "realtime", "atomic_turn"} {
		t.Run(mode, func(t *testing.T) {
			db := setupEmptyStreamRecoveryDB(t)
			setupManagedIntegration(t)
			seedRecoveryPrincipal(t, db)
			stream := mode != "json"
			var mu sync.Mutex
			var calls []string
			var bodies [][]byte
			var headers []http.Header
			failA := false
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				mu.Lock()
				calls = append(calls, r.URL.Path)
				bodies, headers = append(bodies, body), append(headers, r.Header.Clone())
				fail := failA && strings.HasPrefix(r.URL.Path, "/a/")
				id := fmt.Sprintf("resp_upstream_%d", len(calls))
				mu.Unlock()
				if fail {
					if mode == "atomic_turn" {
						w.Header().Set("Content-Type", "text/event-stream")
						_, _ = io.WriteString(w, "data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_failed\"}}\n\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"discard-me\"}\n\n")
					} else {
						w.WriteHeader(429)
						_, _ = io.WriteString(w, `{"error":{"type":"rate_limit_error","message":"busy"}}`)
					}
					return
				}
				managedSuccess(w, id, "answer", stream)
			}))
			t.Cleanup(upstream.Close)
			for i, name := range []string{"a", "b"} {
				ch := recoveryIntegrationChannel(8801+i, name, upstream.URL+"/"+name, int64(20-i), failoverTestModel)
				ch.Setting = common.GetPointer(fmt.Sprintf(`{"conversation_recovery":"managed","client_identity_mode":"per_channel","stream_commit_mode":%q}`, mode))
				if mode == "json" {
					ch.Setting = common.GetPointer(`{"conversation_recovery":"managed","client_identity_mode":"per_channel"}`)
				}
				require.NoError(t, db.Create(ch).Error)
				require.NoError(t, ch.AddAbilities(nil))
			}
			model.InitChannelCache()
			router := managedIntegrationRouter()
			request := func(parent, input string) string {
				payload := map[string]any{"model": failoverTestModel, "input": input, "stream": stream}
				if parent != "" {
					payload["previous_response_id"] = parent
				}
				body, _ := common.Marshal(payload)
				r := httptest.NewRecorder()
				req := httptest.NewRequest("POST", "/v1/responses", bytes.NewReader(body))
				req.Header.Set("Content-Type", "application/json")
				router.ServeHTTP(r, req)
				require.Equal(t, 200, r.Code, r.Body.String())
				require.NotContains(t, r.Body.String(), "discard-me")
				id := gjson.Get(r.Body.String(), "id").String()
				if stream {
					for _, line := range strings.Split(r.Body.String(), "\n") {
						if strings.HasPrefix(line, "data: ") {
							obj := gjson.Parse(strings.TrimPrefix(line, "data: "))
							if obj.Get("type").String() == "response.completed" {
								id = obj.Get("response.id").String()
							}
						}
					}
				}
				require.True(t, strings.HasPrefix(id, service.ManagedResponsePrefix), r.Body.String())
				return id
			}
			first := request("", "first")
			mu.Lock()
			failA = true
			mu.Unlock()
			second := request(first, "second")
			mu.Lock()
			failA = false
			mu.Unlock()
			request(second, "third")
			mu.Lock()
			require.Equal(t, []string{"/a/v1/responses", "/a/v1/responses", "/b/v1/responses", "/b/v1/responses"}, calls)
			require.Equal(t, int64(3), gjson.GetBytes(bodies[2], "input.#").Int())
			require.Equal(t, int64(5), gjson.GetBytes(bodies[3], "input.#").Int())
			require.Empty(t, gjson.GetBytes(bodies[2], "previous_response_id").String())
			require.NotEqual(t, headers[1].Get("X-Codex-Installation-Id"), headers[2].Get("X-Codex-Installation-Id"))
			require.Equal(t, headers[2].Get("Session-Id"), headers[3].Get("Session-Id"))
			mu.Unlock()
			var ledgerCount int64
			require.NoError(t, db.Model(&model.BillingLedger{}).Where("state = ?", model.BillingLedgerStateSettled).Count(&ledgerCount).Error)
			require.EqualValues(t, 3, ledgerCount)
			var commits []model.ConversationTurnCommit
			require.NoError(t, db.Find(&commits).Error)
			require.Len(t, commits, 3)
			for _, commit := range commits {
				require.Equal(t, "delivered", commit.State)
			}
			perfmetrics.WaitForPendingSamples()
		})
	}
}
