package middleware

import (
	_ "embed"
	"html/template"
	"net/http"

	"github.com/gin-gonic/gin"
)

//go:embed auth_error.html
var authErrorHTML string

var authErrorPage = template.Must(template.New("auth-error").Parse(authErrorHTML))

type authentikRejection struct {
	Status   int
	Reason   string
	Message  string
	Title    string
	Detail   string
	NextStep string
}

var (
	untrustedProxy = authentikRejection{
		Status:   http.StatusUnauthorized,
		Reason:   "untrusted_proxy",
		Message:  "请通过 Authentik 登录",
		Title:    "反向代理未受信任",
		Detail:   "面板无法确认当前请求来自可信的反向代理。",
		NextStep: "请联系面板管理员核对日志中的 remote_addr，并检查 authentik.trusted_proxies。",
	}
	invalidIdentity = authentikRejection{
		Status:   http.StatusUnauthorized,
		Reason:   "invalid_identity",
		Message:  "请通过 Authentik 登录",
		Title:    "未收到有效的登录身份",
		Detail:   "面板没有收到可用的 Authentik 用户名。",
		NextStep: "请联系面板管理员检查反向代理转发的 X-Authentik-Username。",
	}
	adminGroupRequired = authentikRejection{
		Status:   http.StatusForbidden,
		Reason:   "admin_group_required",
		Message:  "需要 Authentik 管理员组权限",
		Title:    "需要管理员权限",
		Detail:   "当前登录身份未通过面板的管理员组检查。",
		NextStep: "请联系面板管理员核对你的 Authentik 组权限。",
	}
	crossOriginRequest = authentikRejection{
		Status:  http.StatusForbidden,
		Reason:  "cross_origin_request",
		Message: "拒绝跨站操作请求",
	}
)

func renderAuthentikErrorPage(c *gin.Context, page authentikRejection) {
	c.Header("Content-Type", "text/html; charset=utf-8")
	c.Status(page.Status)
	if c.Request.Method != http.MethodHead {
		_ = authErrorPage.Execute(c.Writer, page)
	}
}
