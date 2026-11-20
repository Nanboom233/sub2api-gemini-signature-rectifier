// sub2api-package: backend/internal/service
package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

const nativeRectifierRejected = `{"error":{"code":400,"message":"Corrupted thought signature."}}`
const nativeRectifierRequest = `{"contents":[{"role":"model","parts":[{"text":"reasoning","thought":true,"thoughtSignature":"old-signature"},{"functionCall":{"name":"tool","args":{"value":17,"thoughtSignature":"business"}},"thoughtSignature":"old-call"}]}]}`
const nativeRectifierSuccess = `{"candidates":[{"content":{"parts":[{"text":"ok"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":7,"candidatesTokenCount":2}}`

type nativeRectifierResponse struct {
	status int
	body   string
	err    error
}

type nativeRectifierCanceledSettingRepo struct {
	SettingRepository
	cancel context.CancelFunc
}

func (r *nativeRectifierCanceledSettingRepo) GetValue(ctx context.Context, _ string) (string, error) {
	r.cancel()
	return "", ctx.Err()
}

type nativeRectifierTrackedBody struct {
	io.Reader
	closed bool
}

func (b *nativeRectifierTrackedBody) Close() error {
	b.closed = true
	return nil
}

type nativeRectifierUpstream struct {
	HTTPUpstream
	responses      []nativeRectifierResponse
	requests       []*http.Request
	bodies         [][]byte
	responseBodies []*nativeRectifierTrackedBody
	after          func(int)
}

func (u *nativeRectifierUpstream) Do(req *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	index := len(u.requests)
	u.requests = append(u.requests, req)
	body, err := io.ReadAll(req.Body)
	if err != nil {
		return nil, err
	}
	u.bodies = append(u.bodies, body)
	if u.after != nil {
		u.after(index)
	}
	if index >= len(u.responses) {
		return nil, fmt.Errorf("unexpected synthetic request %d", index)
	}
	r := u.responses[index]
	if r.err != nil {
		return nil, r.err
	}
	responseBody := &nativeRectifierTrackedBody{Reader: strings.NewReader(r.body)}
	u.responseBodies = append(u.responseBodies, responseBody)
	contentType := "application/json"
	if strings.HasPrefix(r.body, "data:") {
		contentType = "text/event-stream"
	}
	return &http.Response{StatusCode: r.status, Header: http.Header{"Content-Type": {contentType}, "X-Request-Id": {fmt.Sprintf("synthetic-%d", index)}}, Body: responseBody}, nil
}

func newNativeRectifierService(responses ...nativeRectifierResponse) (*GeminiMessagesCompatService, *nativeRectifierUpstream) {
	cfg := &config.Config{}
	repo := &errorPolicyRepoStub{}
	rate := NewRateLimitService(repo, nil, cfg, nil, nil)
	rate.settingService = NewSettingService(&nativeRectifierSettingRepo{value: `{"enabled":true,"gemini_native_signature_enabled":true}`}, cfg)
	upstream := &nativeRectifierUpstream{responses: responses}
	return &GeminiMessagesCompatService{cfg: cfg, accountRepo: repo, rateLimitService: rate, httpUpstream: upstream, tokenProvider: NewGeminiTokenProvider(nil, &fakeGeminiTokenCache{token: "synthetic-token"}, nil)}, upstream
}

func nativeRectifierContext() (*gin.Context, *httptest.ResponseRecorder) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1beta/models/gemini-2.5-flash:generateContent", nil)
	return c, recorder
}

func nativeRectifierKinds(c *gin.Context) []string {
	raw, _ := c.Get(OpsUpstreamErrorsKey)
	events, _ := raw.([]*OpsUpstreamErrorEvent)
	kinds := make([]string, 0, len(events))
	for _, event := range events {
		kinds = append(kinds, event.Kind)
	}
	return kinds
}

func TestNativeRectifierMatcher(t *testing.T) {
	for _, message := range []string{"Invalid thought signature.", "thoughtSignature is missing", "THOUGHT_SIGNATURE"} {
		body, err := json.Marshal(map[string]any{"error": map[string]string{"message": message}})
		require.NoError(t, err)
		require.True(t, isGeminiNativeSignatureRelatedError(body), message)
	}
	require.True(t, isGeminiNativeSignatureRelatedError([]byte(`{"error":{"message":"{\"error\":{\"message\":\"thoughtSignature\"}}"}}`)))
	for _, body := range []string{`{"error":{"message":"Invalid authentication signature"}}`, `{"echo":"thoughtSignature","error":{"message":"Bad input"}}`, `thoughtSignature`, `{}`} {
		require.False(t, isGeminiNativeSignatureRelatedError([]byte(body)), body)
	}
}

func TestNativeRectifierCleaner(t *testing.T) {
	for _, tc := range []struct {
		name, input, want string
		changed           bool
	}{
		{"thought text", `{"contents":[{"parts":[{"text":"  reason  ","thought":true,"thoughtSignature":"old","thought_signature":"old2"}]}]}`, `{"contents":[{"parts":[{"text":"  reason  "}]}]}`, true},
		{"function aliases", `{"contents":[{"parts":[{"function_call":{"name":"f","args":{"n":9007199254740993,"thought_signature":"business"}},"thought_signature":"old"}]}]}`, `{"contents":[{"parts":[{"function_call":{"name":"f","args":{"n":9007199254740993,"thought_signature":"business"}},"thoughtSignature":"skip_thought_signature_validator"}]}]}`, true},
		{"marked carriers", `{"contents":[{"parts":[{"thoughtSignature":"a"},{"text":"","thought":true}]},{"role":"user","parts":[{"text":"keep"}]}]}`, `{"contents":[{"role":"user","parts":[{"text":"keep"}]}]}`, true},
		{"preserve empty and null", `{"contents":[{"parts":null,"role":"user"},{"parts":[]},{"parts":[{"text":"keep","thoughtSignature":"old"}]}]}`, `{"contents":[{"parts":null,"role":"user"},{"parts":[]},{"parts":[{"text":"keep"}]}]}`, true},
		{"other fields", `{"contents":[{"parts":[{"thoughtSignature":"old","payload":{"n":9007199254740993},"thought":false}]}],"systemInstruction":{"parts":[{"text":"keep","thoughtSignature":"outside"}]}}`, `{"contents":[{"parts":[{"payload":{"n":9007199254740993},"thought":false}]}],"systemInstruction":{"parts":[{"text":"keep","thoughtSignature":"outside"}]}}`, true},
		{"canonical", `{"contents":[{"parts":[{"functionCall":{"name":"f"},"thoughtSignature":"skip_thought_signature_validator"}]}]}`, "", false},
		{"unmarked", `{"contents":[{"parts":[{}, {"text":""}, {"thought":false}]}]}`, "", false},
		{"unchanged empty and null", `{"contents":[{"parts":null},{"parts":[]},{"parts":[{"text":"keep"}]}]}`, "", false},
		{"nothing remains", `{"contents":[{"parts":[{"text":"","thought_signature":"a"}]}]}`, "", false},
		{"invalid", `{"contents":`, "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, changed := cleanGeminiNativeThoughtSignaturesForRetry([]byte(tc.input))
			require.Equal(t, tc.changed, changed)
			if !changed {
				require.Equal(t, tc.input, string(out))
				return
			}
			require.JSONEq(t, tc.want, string(out))
			if strings.Contains(tc.input, "9007199254740993") {
				require.Contains(t, string(out), "9007199254740993")
			}
			_, changed = cleanGeminiNativeThoughtSignaturesForRetry(out)
			require.False(t, changed, "a second cleanup must be a no-op")
		})
	}
}

func TestNativeRectifierAllBuilders(t *testing.T) {
	for _, kind := range []string{"relay", "oauth", "codeassist", "vertex"} {
		t.Run(kind, func(t *testing.T) {
			success := nativeRectifierSuccess
			account := &Account{ID: 701, Platform: PlatformGemini, Type: AccountTypeAPIKey, Concurrency: 1, Credentials: map[string]any{"api_key": "synthetic-key", "base_url": "https://relay.example.test"}}
			if kind == "oauth" || kind == "codeassist" {
				account.Type = AccountTypeOAuth
				account.Credentials = map[string]any{"access_token": "synthetic-token"}
			}
			if kind == "codeassist" {
				account.Credentials["project_id"] = "synthetic-project"
				success = "data: {\"response\":" + nativeRectifierSuccess + "}\n\n"
			}
			if kind == "vertex" {
				account = vertexServiceAccount()
				account.ID = 701
			}
			svc, u := newNativeRectifierService(nativeRectifierResponse{status: 400, body: nativeRectifierRejected}, nativeRectifierResponse{status: 200, body: success})
			c, rec := nativeRectifierContext()
			result, err := svc.ForwardNative(context.Background(), c, account, "gemini-2.5-flash", "generateContent", false, []byte(nativeRectifierRequest))
			require.NoError(t, err)
			require.NotNil(t, result)
			require.Equal(t, 200, rec.Code)
			require.Len(t, u.requests, 2)
			require.Equal(t, u.requests[0].URL.String(), u.requests[1].URL.String())
			require.Equal(t, u.requests[0].Header, u.requests[1].Header)
			require.Contains(t, string(u.bodies[0]), "old-signature")
			require.NotContains(t, string(u.bodies[1]), "old-signature")
			require.Contains(t, string(u.bodies[1]), "skip_thought_signature_validator")
			require.Contains(t, string(u.bodies[1]), `"thoughtSignature":"business"`)
			if kind == "codeassist" {
				require.Contains(t, u.requests[1].URL.Path, "v1internal:streamGenerateContent")
				require.Contains(t, string(u.bodies[1]), `"project":"synthetic-project"`)
				require.Contains(t, string(u.bodies[1]), `"request":`)
			}
			require.Equal(t, []string{"signature_error"}, nativeRectifierKinds(c))
			require.Equal(t, "synthetic-1", result.RequestID)
			for _, body := range u.responseBodies {
				require.True(t, body.closed)
			}
		})
	}
}

func TestNativeRectifierCounting(t *testing.T) {
	for _, status := range []int{200, 400} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			body := `{"totalTokens":1234}`
			if status == 400 {
				body = nativeRectifierRejected
			}
			svc, u := newNativeRectifierService(nativeRectifierResponse{status: 400, body: nativeRectifierRejected}, nativeRectifierResponse{status: status, body: body})
			c, rec := nativeRectifierContext()
			_, err := svc.ForwardNative(context.Background(), c, geminiDefaultPolicyAPIKeyAccount(), "gemini-2.5-flash", "countTokens", false, []byte(nativeRectifierRequest))
			require.Len(t, u.requests, 2)
			require.Equal(t, status, rec.Code)
			if status == 200 {
				require.NoError(t, err)
				require.JSONEq(t, body, rec.Body.String())
			} else {
				require.Error(t, err)
				require.NotContains(t, rec.Body.String(), "totalTokens")
				require.Contains(t, nativeRectifierKinds(c), "signature_retry")
				raw, _ := c.Get(OpsUpstreamErrorsKey)
				events := raw.([]*OpsUpstreamErrorEvent)
				require.Equal(t, "signature_error", events[0].Kind)
				require.Equal(t, 400, events[0].UpstreamStatusCode)
				require.Equal(t, "synthetic-0", events[0].UpstreamRequestID)
				require.Equal(t, "signature_retry", events[1].Kind)
				require.Equal(t, 400, events[1].UpstreamStatusCode)
				require.Equal(t, "synthetic-1", events[1].UpstreamRequestID)
			}
		})
	}
}

func TestNativeRectifierStreaming(t *testing.T) {
	body := "data: " + nativeRectifierSuccess + "\n\n"
	svc, upstream := newNativeRectifierService(
		nativeRectifierResponse{status: 400, body: nativeRectifierRejected},
		nativeRectifierResponse{status: 200, body: body},
	)
	c, recorder := nativeRectifierContext()
	result, err := svc.ForwardNative(context.Background(), c, geminiDefaultPolicyAPIKeyAccount(), "gemini-2.5-flash", "streamGenerateContent", true, []byte(nativeRectifierRequest))
	require.NoError(t, err)
	require.True(t, result.Stream)
	require.Contains(t, recorder.Body.String(), `"text":"ok"`)
	require.Len(t, upstream.requests, 2)
	require.Equal(t, []string{"signature_error"}, nativeRectifierKinds(c))
	for _, body := range upstream.responseBodies {
		require.True(t, body.closed)
	}
}

func TestNativeRectifierResendChecksFreshPolicy(t *testing.T) {
	svc, upstream := newNativeRectifierService(
		nativeRectifierResponse{status: 400, body: nativeRectifierRejected},
		nativeRectifierResponse{status: 401, body: geminiSingleCheck401Body},
	)
	repo := &geminiSingleCheckRepoStub{}
	svc.accountRepo = repo
	svc.rateLimitService = NewRateLimitService(repo, nil, svc.cfg, nil, nil)
	svc.rateLimitService.settingService = NewSettingService(&nativeRectifierSettingRepo{value: `{"enabled":true,"gemini_native_signature_enabled":true}`}, svc.cfg)
	c, _ := nativeRectifierContext()
	_, err := svc.ForwardNative(context.Background(), c, geminiAPIKeyAccountWithTempRule(geminiAccountScoped401Rule()), "gemini-2.5-flash", "generateContent", false, []byte(nativeRectifierRequest))
	var failover *UpstreamFailoverError
	require.ErrorAs(t, err, &failover)
	require.Equal(t, 401, failover.StatusCode)
	require.Len(t, upstream.requests, 2)
	require.Equal(t, 1, repo.tempCalls)
	require.Zero(t, repo.setErrCalls)
	require.Equal(t, []string{"signature_error", "signature_retry", "failover"}, nativeRectifierKinds(c))
}

func TestNativeRectifierRecoveryGuards(t *testing.T) {
	for _, tc := range []struct {
		name, body, response string
		status               int
		disabled, policy     bool
	}{
		{"disabled", nativeRectifierRequest, nativeRectifierRejected, 400, true, false},
		{"unchanged", `{"contents":[{"parts":[{"text":"hi"}]}]}`, nativeRectifierRejected, 400, false, false},
		{"invalid JSON", `{"contents":`, nativeRectifierRejected, 400, false, false},
		{"empty cleanup", `{"contents":[{"parts":[{"thought":true,"thoughtSignature":"a"}]}]}`, nativeRectifierRejected, 400, false, false},
		{"unrelated", nativeRectifierRequest, `{"error":{"message":"authentication signature invalid"}}`, 400, false, false},
		{"other status", nativeRectifierRequest, nativeRectifierRejected, 422, false, false},
		{"policy", nativeRectifierRequest, nativeRectifierRejected, 400, false, true},
		{"cached content", nativeRectifierRequest, `{"error":{"message":"Invalid resource state for cache content 8079767076522164224. Invalid thought signature."}}`, 400, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc, u := newNativeRectifierService(nativeRectifierResponse{status: tc.status, body: tc.response})
			if tc.disabled {
				svc.rateLimitService.settingService = NewSettingService(&nativeRectifierSettingRepo{value: `{"enabled":true,"gemini_native_signature_enabled":false}`}, nil)
			}
			account := geminiDefaultPolicyAPIKeyAccount()
			if tc.policy {
				account.Credentials["custom_error_codes_enabled"] = true
				account.Credentials["custom_error_codes"] = []any{float64(422)}
			}
			c, _ := nativeRectifierContext()
			_, err := svc.ForwardNative(context.Background(), c, account, "gemini-2.5-flash", "generateContent", false, []byte(tc.body))
			require.Error(t, err)
			require.Len(t, u.requests, 1)
			require.NotContains(t, nativeRectifierKinds(c), "signature_error")
		})
	}
}

func TestNativeRectifierTransientBudgetAndEvents(t *testing.T) {
	for _, statuses := range [][]int{{503, 400, 200}, {400, 429, 503, 200}} {
		t.Run(fmt.Sprint(statuses), func(t *testing.T) {
			responses := make([]nativeRectifierResponse, 0, len(statuses))
			for _, status := range statuses {
				body := `{"error":{"message":"temporary"}}`
				if status == 400 {
					body = nativeRectifierRejected
				}
				if status == 200 {
					body = nativeRectifierSuccess
				}
				responses = append(responses, nativeRectifierResponse{status: status, body: body})
			}
			svc, u := newNativeRectifierService(responses...)
			svc.cfg.Gateway.MaxSameAccountRetriesGemini = len(statuses) - 2
			c, _ := nativeRectifierContext()
			_, err := svc.ForwardNative(context.Background(), c, geminiDefaultPolicyAPIKeyAccount(), "gemini-2.5-flash", "generateContent", false, []byte(nativeRectifierRequest))
			require.NoError(t, err)
			require.Len(t, u.requests, len(statuses))
			if len(statuses) == 4 {
				require.Equal(t, []string{"signature_error", "signature_retry", "retry", "retry"}, nativeRectifierKinds(c))
				require.Contains(t, string(u.bodies[3]), "skip_thought_signature_validator")
			}
		})
	}
}

func TestNativeRectifierResendFailureAndCancellation(t *testing.T) {
	for _, mode := range []string{"transport", "construction", "cancellation"} {
		t.Run(mode, func(t *testing.T) {
			svc, u := newNativeRectifierService(nativeRectifierResponse{status: 400, body: nativeRectifierRejected}, nativeRectifierResponse{err: errors.New("synthetic transport failure")})
			account := geminiDefaultPolicyAPIKeyAccount()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			u.after = func(index int) {
				if index == 0 && mode == "construction" {
					account.Credentials["api_key"] = ""
				}
				if index == 0 && mode == "cancellation" {
					cancel()
				}
			}
			c, _ := nativeRectifierContext()
			_, err := svc.ForwardNative(ctx, c, account, "gemini-2.5-flash", "generateContent", false, []byte(nativeRectifierRequest))
			require.Error(t, err)
			if mode == "cancellation" {
				require.ErrorIs(t, err, context.Canceled)
				require.Len(t, u.requests, 1)
				require.NotContains(t, nativeRectifierKinds(c), "signature_error")
			} else {
				require.Contains(t, nativeRectifierKinds(c), "signature_retry_request_error")
			}
			require.True(t, u.responseBodies[0].closed)
		})
	}
}

func TestNativeRectifierCancellationDuringSettingsRead(t *testing.T) {
	svc, upstream := newNativeRectifierService(nativeRectifierResponse{status: 400, body: nativeRectifierRejected})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	svc.rateLimitService.settingService = NewSettingService(&nativeRectifierCanceledSettingRepo{cancel: cancel}, svc.cfg)
	c, recorder := nativeRectifierContext()
	_, err := svc.ForwardNative(ctx, c, geminiDefaultPolicyAPIKeyAccount(), "gemini-2.5-flash", "generateContent", false, []byte(nativeRectifierRequest))
	require.ErrorIs(t, err, context.Canceled)
	require.Len(t, upstream.requests, 1)
	require.True(t, upstream.responseBodies[0].closed)
	require.Empty(t, nativeRectifierKinds(c))
	require.Empty(t, recorder.Body.String())
}
