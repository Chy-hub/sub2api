package workbuddy

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/reverse"
)

// These aliases keep the upstream task workflows readable while all transport,
// credentials and persistence continue to be owned by sub2api.
type Credential = credential
type Region = region

const (
	RegionCN             = regionCN
	RegionGlobal         = regionGlobal
	DesktopClientVersion = clientVersion
	defaultCLIVersion    = cliVersion
	chatHeaderTimeout    = 120 * time.Second
	chromeUA             = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/152.0.0.0 Safari/537.36"
)

func (r region) IsGlobal() bool                 { return r.isGlobal() }
func (c *credential) Realm() Region             { return c.region }
func (c *credential) AccessTokenValue() string  { return c.accessToken }
func (c *credential) RefreshTokenValue() string { return c.refreshToken }
func (c *credential) DomainValue() string       { return c.domain }
func (c *credential) UIDValue() string          { return c.uid }
func (c *credential) NicknameValue() string     { return c.nickname }
func (c *credential) EnterpriseIDValue() string { return c.enterpriseID }
func (c *credential) DeviceTokenValue() string  { return c.deviceToken }
func RealmDisplayName(r Region) string {
	if r.isGlobal() {
		return "WorkBuddy Global"
	}
	return "WorkBuddy CN"
}

type Endpoints struct{ ChatBase, BillingBase, WebBase string }

func GetEndpoints(r Region) Endpoints {
	ep := endpointsFor(r)
	return Endpoints{ChatBase: ep.chatBase, BillingBase: ep.billingBase, WebBase: ep.chatBase}
}

type runtime struct{ cred *credential }

func (r *runtime) deriveID(uid, purpose string) string {
	return deriveID(uid, purpose, r.cred.machineSalt)
}
func (r *runtime) fingerprint(c *Credential, purpose string) string {
	if purpose == "machine" {
		return c.machineID()
	}
	if purpose == "session" {
		return c.sessionID()
	}
	return r.deriveID(c.uid, purpose)
}

type Client struct {
	provider *Provider
	acc      *reverse.Account
	rt       *runtime
	opts     struct{ Context context.Context }
}

func (c *Client) enabledRealm(Region) bool { return true }
func (c *Client) shortClient() *http.Client {
	client := *c.provider.client(c.acc)
	client.Timeout = shortCallTimeout
	return &client
}
func (c *Client) chatClient(time.Duration) *http.Client { return c.provider.client(c.acc) }
func (c *Client) doJSON(req *http.Request) ([]byte, error) {
	return apiCall(c.shortClient(), req, apiBodyLimit)
}
func (c *Client) applyBillingHeaders(req *http.Request, cred *Credential) {
	applyBillingHeaders(req, cred)
}
func closeBody(body interface{ Close() error }) {
	if body != nil {
		_ = body.Close()
	}
}

type taskAdapter struct {
	p      *Provider
	acc    *reverse.Account
	region Region
}

func (v *taskAdapter) Native(acc *reverse.Account) (*Credential, error) {
	if acc == nil {
		return nil, fmt.Errorf("workbuddy: account is required")
	}
	return newCredential(acc), nil
}
func (v *taskAdapter) client(ctx context.Context) *Client {
	c := &Client{provider: v.p, acc: v.acc, rt: &runtime{cred: newCredential(v.acc)}}
	c.opts.Context = ctx
	return c
}
func (p *Provider) tasks(acc *reverse.Account) *taskAdapter {
	return &taskAdapter{p: p, acc: acc, region: newCredential(acc).region}
}
func (p *Provider) TaskSpecs(acc *reverse.Account) []reverse.TaskSpec {
	return p.tasks(acc).TaskSpecs()
}
func (p *Provider) InspectTasks(ctx context.Context, acc *reverse.Account) (reverse.TaskInspection, error) {
	return p.tasks(acc).InspectTasks(ctx, acc)
}
func (p *Provider) RunTask(ctx context.Context, acc *reverse.Account, code string) (reverse.TaskRunResult, error) {
	return p.tasks(acc).RunTask(ctx, acc, code)
}
func (p *Provider) RunAllTasks(ctx context.Context, acc *reverse.Account) []reverse.AutoTaskResult {
	return p.tasks(acc).RunAllTasks(ctx, acc)
}
func (p *Provider) RunScheduled(ctx context.Context, acc *reverse.Account, kind string) (string, error) {
	return p.tasks(acc).RunScheduled(ctx, acc, kind)
}
func (p *Provider) TaskStatus(ctx context.Context, acc *reverse.Account) reverse.TaskStatusResult {
	return p.tasks(acc).TaskStatus(ctx, acc)
}
func (p *Provider) Quota(ctx context.Context, acc *reverse.Account) (*reverse.QuotaFetchResponse, error) {
	c := p.tasks(acc).client(ctx)
	cred := newCredential(acc)
	balance, err := c.FetchBalance(cred)
	if err != nil {
		return nil, err
	}
	result := BuildQuotaResponse(cred, balance)
	return &result, nil
}

func transientError(err error) error { return fmt.Errorf("workbuddy transport: %w", err) }
func enrichClassifyError(status int, body string, header http.Header) *reverse.UpstreamError {
	return upstreamError(status, body, header)
}
