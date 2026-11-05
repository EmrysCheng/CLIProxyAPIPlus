package sourceupdate

import (
	"errors"
	"net/url"
	"regexp"
	"strings"
)

func validBuildRegistry(value string) bool {
	u, err := url.Parse(value)
	return err == nil && u.Scheme == "https" && u.Host != "" && u.User == nil && u.RawQuery == "" && u.Fragment == "" && !strings.ContainsAny(value, "\r\n\x00")
}
func validateBuildNetwork(tools Tools) error {
	if len(tools.GoProxy) > 2048 || len(tools.NpmRegistry) > 2048 {
		return errors.New("build network setting is too long")
	}
	if tools.GoProxy != "" {
		for _, value := range regexp.MustCompile(`[,|]`).Split(tools.GoProxy, -1) {
			if value != "direct" && value != "off" && !validBuildRegistry(value) {
				return errors.New("GOPROXY must contain trusted HTTPS URLs, direct or off; credentials are not accepted")
			}
		}
	}
	if tools.NpmRegistry != "" && !validBuildRegistry(tools.NpmRegistry) {
		return errors.New("npm registry must be a trusted HTTPS URL without credentials")
	}
	return nil
}
