package service

import (
	"crypto/tls"
	"fmt"
	"net"
	"net/smtp"
	"yagp/internal/dto"
	"yagp/internal/errors"
)

// SendTestEmail 发送测试邮件
func (s *SystemConfigService) SendTestEmail(req *dto.EmailConfigReq) error {
	// 验证必要参数
	if req.Host == "" || req.Port == 0 || req.FromEmail == "" {
		return errors.ErrSMTPConfigIncomplete
	}

	auth := smtp.PlainAuth("", req.Username, req.Password, req.Host)

	toEmail := req.FromEmail
	if req.ToEmail != "" {
		toEmail = req.ToEmail
	}

	to := []string{toEmail}

	msg := []byte(fmt.Sprintf("To: %s\r\n"+
		"Subject: yagp 测试邮件\r\n"+
		"\r\n"+
		"这是一封来自 yagp 的测试邮件。\r\n"+
		"如果您收到这封邮件，说明您的 SMTP 配置正确。\r\n", toEmail))

	addr := fmt.Sprintf("%s:%d", req.Host, req.Port)

	// 如果端口是 465，通常使用 SMTPS (隐式 TLS)
	// 如果端口是 587，通常使用 STARTTLS

	if req.Port == 465 {
		// 隐式 TLS 在发送 AUTH 前使用系统信任根校验证书链和 SMTP 主机名。
		tlsConfig := &tls.Config{
			MinVersion: tls.VersionTLS12,
			ServerName: req.Host,
		}

		conn, err := tls.Dial("tcp", addr, tlsConfig)
		if err != nil {
			return errors.ErrSMTPConnectFailed
		}

		return sendSMTPTestMessage(conn, req, toEmail, msg)

	}

	// 标准 SMTP / STARTTLS
	return smtp.SendMail(addr, auth, req.FromEmail, to, msg)
}

// The SMTP transaction owns the connection, including rejected server greetings.
func sendSMTPTestMessage(conn net.Conn, req *dto.EmailConfigReq, recipient string, msg []byte) error {
	defer conn.Close()
	client, err := smtp.NewClient(conn, req.Host)
	if err != nil {
		return errors.ErrSMTPClientFailed
	}
	if req.Username != "" && req.Password != "" {
		auth := smtp.PlainAuth("", req.Username, req.Password, req.Host)
		if err := client.Auth(auth); err != nil {
			return errors.ErrSMTPAuthFailed
		}
	}
	if err := client.Mail(req.FromEmail); err != nil {
		return errors.ErrSMTPSenderFailed
	}
	if err := client.Rcpt(recipient); err != nil {
		return errors.ErrSMTPRecipientFailed
	}
	w, err := client.Data()
	if err != nil {
		return errors.ErrSMTPDataFailed
	}
	if _, err := w.Write(msg); err != nil {
		return errors.ErrSMTPWriteFailed
	}
	if err := w.Close(); err != nil {
		return errors.ErrSMTPCloseFailed
	}
	return client.Quit()
}
