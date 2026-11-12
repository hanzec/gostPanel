package service

import (
	"net/http"

	"yagp/internal/errors"
	"yagp/internal/model"
	"yagp/pkg/gost"
)

func validateRelayProtocolOptions(protocol string, options map[string]string) error {
	if err := gost.ValidateRelayOptions(protocol, options); err != nil {
		return errors.New(40000, err.Error(), http.StatusBadRequest)
	}
	return nil
}

func validateEndpointProtocolOptions(role model.EndpointRole, protocol string, options map[string]string) error {
	if len(options) != 0 && role != model.EndpointRoleExit {
		return errors.New(40000, "隧道协议参数只能配置在出口端点", http.StatusBadRequest)
	}
	return validateRelayProtocolOptions(protocol, options)
}
