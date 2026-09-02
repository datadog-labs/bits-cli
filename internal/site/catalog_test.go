package site

import (
	"reflect"
	"testing"
)

func TestLoginRegions(t *testing.T) {
	got := LoginRegions()
	want := []Region{
		{Name: "US1", WebHost: "app.datadoghq.com"},
		{Name: "US3", WebHost: "us3.datadoghq.com"},
		{Name: "US5", WebHost: "us5.datadoghq.com"},
		{Name: "EU1", WebHost: "app.datadoghq.eu"},
		{Name: "AP1", WebHost: "ap1.datadoghq.com"},
		{Name: "AP2", WebHost: "ap2.datadoghq.com"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("LoginRegions() = %#v, want %#v", got, want)
	}
}

func TestWebHostForAssistantHost(t *testing.T) {
	tests := []struct {
		host string
		want string
		ok   bool
	}{
		{host: "api.datadoghq.com", want: "app.datadoghq.com", ok: true},
		{host: "API.US3.DATADOGHQ.COM", want: "us3.datadoghq.com", ok: true},
		{host: "api.datad0g.com", want: "dd.datad0g.com", ok: true},
		{host: "dd.datad0g.com", want: "dd.datad0g.com", ok: true},
		{host: "api.ddog-gov.com"},
	}
	for _, test := range tests {
		t.Run(test.host, func(t *testing.T) {
			got, ok := WebHostForAssistantHost(test.host)
			if got != test.want || ok != test.ok {
				t.Fatalf("WebHostForAssistantHost() = %q, %t, want %q, %t", got, ok, test.want, test.ok)
			}
		})
	}
}
