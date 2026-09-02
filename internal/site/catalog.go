// Package site contains the Datadog regional endpoints used by Bits CLI.
package site

import "strings"

// Region describes a Datadog region shown in the login picker.
type Region struct {
	Name    string
	WebHost string
}

type catalogEntry struct {
	Region
	AssistantHosts []string
	ShowInLogin    bool
}

// TODO: Keep this catalog in sync with Datadog's public site list:
// https://docs.datadoghq.com/getting_started/site/. When adding a region, add
// its login and Assistant hosts here and extend catalog_test.go.
var regions = []catalogEntry{
	{Region: Region{Name: "US1", WebHost: "app.datadoghq.com"}, AssistantHosts: []string{"api.datadoghq.com"}, ShowInLogin: true},
	{Region: Region{Name: "US3", WebHost: "us3.datadoghq.com"}, AssistantHosts: []string{"api.us3.datadoghq.com"}, ShowInLogin: true},
	{Region: Region{Name: "US5", WebHost: "us5.datadoghq.com"}, AssistantHosts: []string{"api.us5.datadoghq.com"}, ShowInLogin: true},
	{Region: Region{Name: "EU1", WebHost: "app.datadoghq.eu"}, AssistantHosts: []string{"api.datadoghq.eu"}, ShowInLogin: true},
	{Region: Region{Name: "AP1", WebHost: "ap1.datadoghq.com"}, AssistantHosts: []string{"api.ap1.datadoghq.com"}, ShowInLogin: true},
	{Region: Region{Name: "AP2", WebHost: "ap2.datadoghq.com"}, AssistantHosts: []string{"api.ap2.datadoghq.com"}, ShowInLogin: true},
	{Region: Region{Name: "Staging", WebHost: "dd.datad0g.com"}, AssistantHosts: []string{"api.datad0g.com", "dd.datad0g.com"}},
}

// LoginRegions returns the regions shown in the interactive login picker.
func LoginRegions() []Region {
	result := make([]Region, 0, len(regions))
	for _, region := range regions {
		if region.ShowInLogin {
			result = append(result, region.Region)
		}
	}
	return result
}

// WebHostForAssistantHost resolves an Assistant API host to its regional web
// application host. It returns false for sites outside the catalog.
func WebHostForAssistantHost(host string) (string, bool) {
	host = strings.ToLower(strings.TrimSpace(host))
	for _, region := range regions {
		for _, assistantHost := range region.AssistantHosts {
			if host == assistantHost {
				return region.WebHost, true
			}
		}
	}
	return "", false
}
