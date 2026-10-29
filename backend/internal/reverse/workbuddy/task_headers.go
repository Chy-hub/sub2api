package workbuddy

import (
	"net/http"
	"strings"
)

func applyMPHeaders(req *http.Request) {
	req.Header.Set("X-Client-Product", "workbuddy-mp")
	req.Header.Set("X-Client-Version", "2.4.0")
	req.Header.Set("X-Client-Platform", "mp-weixin")
	req.Header.Set("X-Platform", "wechatmp")
}

// applyDesktopHeaders 写入桌面指纹族请求的头。
//
// 桌面族用于成长任务的埋点上报：上游按 UA 与 X-Domain 把事件归属到
// "桌面客户端"，与 CLI 族的事件分开计分。
func (c *Client) applyDesktopHeaders(req *http.Request, cred *Credential) {
	applyCommonHeaders(req, cred)
	applyAuthHeaders(req, cred)

	req.Header.Set("Accept", "application/json, text/plain, */*")
	req.Header.Set("Content-Type", "application/json;charset=UTF-8")
	req.Header.Set("User-Agent", cred.desktopUA())
	req.Header.Set("X-Domain", strings.TrimSuffix(GetEndpoints(cred.Realm()).ChatBase, "/"))
	req.Header.Set("X-Product", "SaaS")
	req.Header.Set("X-Request-ID", requestIDFor(cred, c.rt))
}

// desktopUA 返回桌面指纹族的 UA。
func (cred *credential) desktopUA() string {
	return firstNonEmpty(cred.userAgent, "WorkBuddy/"+cred.desktopVersion+" WorkBuddy/"+cred.desktopVersion+" CLI/"+cred.cliVersion)
}

// requestIDFor 生成一个带纳秒后缀的请求 ID（桌面族要求每次不同）。
func requestIDFor(cred *Credential, rt *runtime) string {
	return rt.deriveID(cred.UIDValue(), "req") + newHexID()[:6]
}

// applyWebHeaders 写入 web 指纹族请求的头（浏览器形态）。
func applyWebHeaders(req *http.Request, cred *Credential, pageURL string) {
	endpoints := GetEndpoints(RegionCN)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/plain, */*")
	req.Header.Set("Origin", endpoints.WebBase)
	req.Header.Set("Referer", pageURL)
	req.Header.Set("User-Agent", chromeUA)
	req.Header.Set("x-client-platform", "web")
	applyAuthHeaders(req, cred)
	if enterpriseID := cred.EnterpriseIDValue(); enterpriseID != "" {
		req.Header.Set("X-Enterprise-Id", enterpriseID)
		req.Header.Set("X-Tenant-Id", enterpriseID)
	}
	if domain := cred.DomainValue(); domain != "" {
		req.Header.Set("X-Domain", domain)
	}
}
