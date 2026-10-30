//go:build integration

package repository

import (
	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

func (s *AccountRepoSuite) TestListWithFiltersProxyName() {
	first := mustCreateProxy(s.T(), s.client, &service.Proxy{Name: "Office proxy", Protocol: "http", Host: "192.0.2.10", Port: 8080})
	second := mustCreateProxy(s.T(), s.client, &service.Proxy{Name: "second", Protocol: "http", Host: "198.51.100.20", Port: 8080})
	a := mustCreateAccount(s.T(), s.client, &service.Account{Name: "first-account", Platform: service.PlatformOpenAI, ProxyID: &first.ID})
	b := mustCreateAccount(s.T(), s.client, &service.Account{Name: "second-account", Platform: service.PlatformOpenAI, ProxyID: &second.ID})
	direct := mustCreateAccount(s.T(), s.client, &service.Account{Name: "direct-account", Platform: service.PlatformOpenAI})
	for _, tc := range []struct {
		query string
		ids   []int64
	}{
		{"Office proxy", []int64{a.ID}},
		{"second", []int64{b.ID}},
		{"unknown proxy", []int64{}},
		{"Office", []int64{}},
		{"192.0.2.10", []int64{}},
		{"", []int64{a.ID, b.ID, direct.ID}},
	} {
		s.Run(tc.query, func() {
			for _, lite := range []bool{false, true} {
				accounts, page, err := s.repo.listWithFilters(s.ctx, pagination.PaginationParams{Page: 1, PageSize: 10}, "", "", "", "", 0, "", 0, lite, service.AccountListOptions{ProxyName: tc.query})
				s.Require().NoError(err)
				ids := make([]int64, 0, len(accounts))
				for _, account := range accounts {
					ids = append(ids, account.ID)
				}
				s.Require().ElementsMatch(tc.ids, ids)
				s.Require().Equal(int64(len(tc.ids)), page.Total)
			}
		})
	}
	// Apply the filter before counting and pagination, and combine it with other filters.
	accounts, page, err := s.repo.ListWithFilters(s.ctx, pagination.PaginationParams{Page: 1, PageSize: 1}, service.PlatformAnthropic, "", "", "", 0, "", 0, service.AccountListOptions{ProxyName: "Office proxy"})
	s.Require().NoError(err)
	s.Require().Empty(accounts)
	s.Require().Zero(page.Total)
}
