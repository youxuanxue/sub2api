package service

import "net/http"

func (s *OpenAIGatewayService) doNativeMessagesRequest(req *http.Request, account *Account) (*http.Response, error) {
	proxyURL := ""
	if account.Proxy != nil {
		proxyURL = account.Proxy.URL()
	}
	return s.doOpenAIUpstream(req, proxyURL, account)
}
