package controller

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
	"gorm.io/gorm"
)

type accountPoolCapture struct {
	Account, Step string
	Header        http.Header
	Body          []byte
}

func accountPoolGateway(t *testing.T, db *gorm.DB) *httptest.Server {
	t.Helper()
	require.NoError(t, i18n.Init())
	model.InitChannelCache()
	router := gin.New()
	router.Use(middleware.BodyStorageCleanup(), middleware.TokenAuth(), func(c *gin.Context) {
		common.SetContextKey(c, common.RequestIdKey, uuid.NewString())
		c.Next()
	})
	router.POST("/v1/responses", middleware.ConversationContext(), middleware.Distribute(), func(c *gin.Context) {
		Relay(c, types.RelayFormatOpenAIResponses)
		if turn := service.GetConversationTurn(c); turn != nil && turn.Parent != nil {
			t.Logf("parent account=%s input=%d delta=%d attempts=%d attempt=%v", turn.Parent.AccountID, len(turn.Input), len(turn.Delta), turn.Attempts, turn.Attempt != nil)
		}
	})
	router.GET("/v1/responses", ResponsesWebSocket)
	router.GET("/v1/rillven/conversations", ListOwnedConversations)
	router.DELETE("/v1/rillven/conversations/:id", DeleteOwnedConversation)
	server := httptest.NewServer(router)
	t.Cleanup(server.Close)
	return server
}

func TestAccountPoolTwoAccountsTwoMembersActualHTTPAndNativeWS(t *testing.T) {
	for _, native := range []bool{false, true} {
		t.Run(fmt.Sprintf("native_ws=%v", native), func(t *testing.T) {
			db := setupEmptyStreamRecoveryDB(t)
			setupManagedIntegration(t)
			require.NoError(t, model.MigrateUpstreamAccounts())
			require.NoError(t, model.MigrateUpstreamCredentials())
			require.NoError(t, model.MigrateUpstreamAdmission())
			require.NoError(t, model.MigrateAccountQuota())
			require.NoError(t, db.AutoMigrate(&model.CodexRefreshJournal{}))
			seedRecoveryPrincipal(t, db)
			require.NoError(t, db.Model(&model.Token{}).Where("id = ?", 1).Update("key", "poolone").Error)
			require.NoError(t, db.Create(&model.User{Id: 2, Username: "pool-two", Password: "test-password", Status: common.UserStatusEnabled, Quota: 1000000, Group: "default", AffCode: "pool-two"}).Error)
			require.NoError(t, db.Create(&model.Token{Id: 2, UserId: 2, Key: "pooltwo", Name: "pool-two", Status: common.TokenStatusEnabled, RemainQuota: 1000000}).Error)
			common.RetryTimes = 2
			var mu sync.Mutex
			var captures []accountPoolCapture
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var conn *websocket.Conn
				if native {
					var err error
					conn, err = (&websocket.Upgrader{}).Upgrade(w, r, nil)
					if err != nil {
						return
					}
					defer conn.Close()
				}
				for {
					var body []byte
					var err error
					if native {
						_, body, err = conn.ReadMessage()
					} else {
						body, err = io.ReadAll(r.Body)
					}
					if err != nil {
						return
					}
					step := gjson.GetBytes(body, "client_metadata.test_step").String()
					account := r.Header.Get("Chatgpt-Account-Id")
					mu.Lock()
					captures = append(captures, accountPoolCapture{Account: account, Step: step, Header: r.Header.Clone(), Body: append([]byte(nil), body...)})
					mu.Unlock()
					var output []any
					switch step {
					case "one-first", "two-first":
						output = []any{map[string]any{"type": "function_call", "id": "fc_" + step, "call_id": "call_" + step, "name": "lookup", "namespace": "functions", "arguments": `{"n":9007199254740993}`, "status": "completed"}}
					case "one-second":
						output = []any{map[string]any{"type": "custom_tool_call", "id": "ct_" + step, "call_id": "call_" + step, "name": "lookup", "input": "literal tool text", "status": "completed"}}
					default:
						output = []any{map[string]any{"type": "message", "id": "msg_" + step, "role": "assistant", "status": "completed", "content": []any{map[string]string{"type": "output_text", "text": "done"}}}}
					}
					response := map[string]any{"id": "resp_" + step, "status": "completed", "model": failoverTestModel, "output": output, "usage": map[string]int{"input_tokens": 11, "output_tokens": 3, "total_tokens": 14}}
					if step == "one-second" {
						reset := time.Now().Add(time.Hour).Unix()
						if native {
							quota, _ := common.Marshal(map[string]any{"type": "codex.rate_limits", "rate_limits": map[string]any{"primary": map[string]any{"used_percent": 100, "reset_at": reset}}})
							if conn.WriteMessage(websocket.TextMessage, quota) != nil {
								return
							}
						} else {
							w.Header().Set("x-codex-primary-used-percent", "100")
							w.Header().Set("x-codex-primary-reset-at", fmt.Sprint(reset))
						}
					}
					data, _ := common.Marshal(map[string]any{"type": "response.completed", "stream_id": gjson.GetBytes(body, "stream_id").String(), "response": response})
					if native {
						if conn.WriteMessage(websocket.TextMessage, data) != nil {
							return
						}
					} else {
						w.Header().Set("Content-Type", "text/event-stream")
						_, _ = fmt.Fprintf(w, "data: %s\n\n", data)
						return
					}
				}
			}))
			t.Cleanup(upstream.Close)
			for index, account := range []string{"a", "b"} {
				credential, _ := common.Marshal(service.CodexOAuthKey{AccountID: account, IdentityPrincipal: "principal-" + account, AccessToken: "access-" + account, RefreshToken: "refresh-" + account})
				ch := recoveryIntegrationChannel(9101+index, account, upstream.URL, int64(20-index), failoverTestModel)
				ch.Type, ch.Key = constant.ChannelTypeCodex, string(credential)
				ch.Setting = common.GetPointer(fmt.Sprintf(`{"client_identity_mode":"per_account","conversation_recovery":"managed","stream_commit_mode":"atomic_turn","responses_websocket":%v}`, native))
				require.NoError(t, db.Create(ch).Error)
				require.NoError(t, ch.AddAbilities(nil))
			}
			gateway := accountPoolGateway(t, db)
			connections := make(map[string]*websocket.Conn)
			request := func(member, step, parent string, input any) (string, gjson.Result) {
				payload := map[string]any{"model": failoverTestModel, "input": input, "stream": native, "client_metadata": map[string]any{"session_id": "shared-client-thread", "thread_id": "shared-client-thread", "test_step": step, "turn_id": "turn-" + step}}
				payload["tools"] = []any{map[string]any{"type": "namespace", "name": "functions", "tools": []any{map[string]any{"type": "function", "name": "lookup", "parameters": map[string]any{"type": "object"}}}}, map[string]any{"type": "custom", "name": "lookup"}}
				if parent != "" {
					payload["previous_response_id"] = parent
				}
				var result gjson.Result
				if native {
					payload["idempotency_key"] = step
					conn := connections[member]
					if conn == nil {
						var err error
						conn, _, err = websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(gateway.URL, "http")+"/v1/responses", http.Header{"Authorization": {"Bearer sk-pool" + member}})
						require.NoError(t, err)
						connections[member] = conn
						t.Cleanup(func() { _ = conn.Close() })
					}
					payload["type"], payload["stream_id"] = "response.create", "main"
					sendResponsesWSTest(t, conn, payload)
					_, terminal := readResponsesWSTerminal(t, conn)
					if terminal.Get("type").String() == "error" {
						return "", terminal
					}
					result = terminal.Get("response")
				} else {
					body, _ := common.Marshal(payload)
					req, _ := http.NewRequest("POST", gateway.URL+"/v1/responses", bytes.NewReader(body))
					req.Header.Set("Authorization", "Bearer sk-pool"+member)
					req.Header.Set("Content-Type", "application/json")
					req.Header.Set("Idempotency-Key", step)
					resp, err := gateway.Client().Do(req)
					require.NoError(t, err)
					data, err := io.ReadAll(resp.Body)
					_ = resp.Body.Close()
					require.NoError(t, err)
					result = gjson.ParseBytes(data)
				}
				return result.Get("id").String(), result
			}
			first, result := request("one", "one-first", "", "first")
			require.NotEmpty(t, first, result.Raw)
			repeated, result := request("one", "one-first", "", "first")
			require.Equal(t, first, repeated, result.Raw)
			_, conflict := request("one", "one-first", "", "changed")
			require.NotEmpty(t, conflict.Get("error").Raw)
			other, result := request("two", "two-first", "", "other")
			require.NotEmpty(t, other, result.Raw)
			_, result = request("two", "denied", first, "stolen context")
			require.NotEmpty(t, result.Get("error").Raw, result.Raw)
			require.NotEqual(t, "model_capacity", result.Get("error.code").String(), "owner rejection keeps its actionable local category")
			second, result := request("one", "one-second", first, []any{map[string]string{"type": "function_call_output", "call_id": "call_one-first", "output": "result one"}})
			require.NotEmpty(t, second, result.Raw)
			// A new gateway instance takes over the same committed database state.
			for member, conn := range connections {
				_ = conn.Close()
				delete(connections, member)
			}
			service.ClearChannelAffinityCacheAll()
			gateway = accountPoolGateway(t, db)
			next, result := request("two", "two-next", other, []any{map[string]string{"type": "function_call_output", "call_id": "call_two-first", "output": "other result"}})
			require.NotEmpty(t, next, result.Raw)
			third, result := request("one", "one-third", second, []any{map[string]string{"type": "custom_tool_call_output", "call_id": "call_one-second", "output": "result two"}})
			require.NotEmpty(t, third, result.Raw)
			mu.Lock()
			defer mu.Unlock()
			require.Len(t, captures, 5, "foreign context must never reach upstream")
			require.Equal(t, []string{"a", "a", "a", "b", "b"}, []string{captures[0].Account, captures[1].Account, captures[2].Account, captures[3].Account, captures[4].Account})
			require.Equal(t, captures[0].Header.Get("X-Codex-Installation-Id"), captures[1].Header.Get("X-Codex-Installation-Id"))
			require.NotEqual(t, captures[0].Header.Get("Session-Id"), captures[1].Header.Get("Session-Id"))
			require.NotEqual(t, captures[0].Header.Get("X-Codex-Installation-Id"), captures[3].Header.Get("X-Codex-Installation-Id"))
			require.NotEqual(t, gjson.GetBytes(captures[3].Body, "client_metadata.thread_id").String(), gjson.GetBytes(captures[4].Body, "client_metadata.thread_id").String())
			for _, captured := range captures {
				require.Equal(t, captured.Header.Get("X-Codex-Installation-Id"), gjson.GetBytes(captured.Body, "client_metadata.x-codex-installation-id").String())
				require.Equal(t, captured.Header.Get("Thread-Id"), gjson.GetBytes(captured.Body, "client_metadata.thread_id").String())
				require.Equal(t, captured.Header.Get("X-Client-Request-Id"), captured.Header.Get("Thread-Id"))
				require.Equal(t, "codex_cli_rs", captured.Header.Get("Originator"))
			}
			require.Len(t, gjson.GetBytes(captures[4].Body, "input").Array(), 5)
			require.Equal(t, "call_one-first", gjson.GetBytes(captures[4].Body, "input.1.call_id").String())
			require.Equal(t, "call_one-second", gjson.GetBytes(captures[4].Body, "input.4.call_id").String())
			require.Equal(t, "9007199254740993", gjson.GetBytes(captures[4].Body, "input.1.arguments.n").Raw)
			require.False(t, gjson.GetBytes(captures[4].Body, "input.1.id").Exists())
		})
	}
}
