package appstore

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func gz(t *testing.T, s string) []byte {
	t.Helper()
	var b bytes.Buffer
	w := gzip.NewWriter(&b)
	io.WriteString(w, s)
	w.Close()
	return b.Bytes()
}

const salesTSV = "Provider\tSKU\tTitle\tProduct Type Identifier\tUnits\tDeveloper Proceeds\tCurrency of Proceeds\tApple Identifier\tParent Identifier\n" +
	"APPLE\tlexify\tLexify\t1F\t12\t0\tUSD\t111\t\n" +
	"APPLE\tlexify\tLexify\t3F\t2\t0\tUSD\t111\t\n" +
	"APPLE\tlexify\tLexify\t7F\t30\t0\tUSD\t111\t\n" +
	"APPLE\tlexify.pro\tLexify Pro\tIAY\t3\t2.10\tEUR\t999\tlexify\n" +
	"APPLE\tlexify.pro\tLexify Pro\tIAY\t-1\t2.10\tEUR\t999\tlexify\n" +
	"APPLE\tother\tOther\t1\t5\t0\tUSD\t222\t\n"

const engagementCSV = "Date,App Name,App Apple Identifier,Event,Page Type,Source Type,Engagement Type,Device,Platform Version,Territory,Counts,Unique Counts\n" +
	"2026-10-12,Lexify,111,Impression,No page,App Store search,,iPhone,iOS 26,CZ,100,80\n" +
	"2026-10-12,Lexify,111,Page view,Product page,App Store search,,iPhone,iOS 26,CZ,20,15\n" +
	"2026-10-12,Lexify,111,Impression,No page,App Store browse,,iPad,iOS 26,DE,20,20\n" +
	"2026-10-01,Lexify,111,Impression,No page,App Store browse,,iPad,iOS 26,DE,999,999\n"

const downloadsTSV = "Date\tApp Name\tApp Apple Identifier\tDownload Type\tCounts\n" +
	"2026-10-12\tLexify\t111\tFirst-time download\t6\n" +
	"2026-10-12\tLexify\t111\tRedownload\t3\n" +
	"2026-10-12\tLexify\t111\tAuto-update\t40\n"

func reportServer(t *testing.T) (*Client, *httptest.Server) {
	t.Helper()
	var srv *httptest.Server
	mux := http.NewServeMux()
	auth := func(w http.ResponseWriter, r *http.Request) bool {
		if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
			http.Error(w, "no token", 401)
			return false
		}
		return true
	}
	js := func(w http.ResponseWriter, v any) { json.NewEncoder(w).Encode(v) }
	mux.HandleFunc("/v1/salesReports", func(w http.ResponseWriter, r *http.Request) {
		if !auth(w, r) {
			return
		}
		q := r.URL.Query()
		if q.Get("filter[vendorNumber]") != "8800" || q.Get("filter[reportType]") != "SALES" || q.Get("filter[version]") != "1_0" {
			http.Error(w, "bad filter", 400)
			return
		}
		if q.Get("filter[reportDate]") != "2026-10-12" {
			http.Error(w, `{"errors":[{"status":"404"}]}`, 404)
			return
		}
		w.Write(gz(t, salesTSV))
	})
	mux.HandleFunc("/v1/apps/111", func(w http.ResponseWriter, r *http.Request) {
		js(w, map[string]any{"data": map[string]any{"attributes": map[string]any{"sku": "lexify", "name": "Lexify"}}})
	})
	mux.HandleFunc("/v1/apps/111/analyticsReportRequests", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("filter[accessType]") != "ONGOING" {
			http.Error(w, "bad filter", 400)
			return
		}
		js(w, map[string]any{"data": []any{
			map[string]any{"id": "old", "attributes": map[string]any{"stoppedDueToInactivity": true}},
			map[string]any{"id": "req1", "attributes": map[string]any{"stoppedDueToInactivity": false}},
		}})
	})
	mux.HandleFunc("/v1/analyticsReportRequests/req1/reports", func(w http.ResponseWriter, r *http.Request) {
		js(w, map[string]any{"data": []any{
			map[string]any{"id": "rEng", "attributes": map[string]any{"name": "App Store Discovery and Engagement Standard"}},
			map[string]any{"id": "rEngD", "attributes": map[string]any{"name": "App Store Discovery and Engagement Detailed"}},
			map[string]any{"id": "rDl", "attributes": map[string]any{"name": "App Store Downloads Standard"}},
		}})
	})
	inst := func(id, date string) map[string]any {
		return map[string]any{"id": id, "attributes": map[string]any{"granularity": "DAILY", "processingDate": date}}
	}
	mux.HandleFunc("/v1/analyticsReports/rEng/instances", func(w http.ResponseWriter, r *http.Request) {
		js(w, map[string]any{"data": []any{inst("iOld", "2026-10-02"), inst("iEng", "2026-10-13")}})
	})
	mux.HandleFunc("/v1/analyticsReports/rDl/instances", func(w http.ResponseWriter, r *http.Request) {
		js(w, map[string]any{"data": []any{inst("iDl", "2026-10-13")}})
	})
	seg := func(name string) func(http.ResponseWriter, *http.Request) {
		return func(w http.ResponseWriter, r *http.Request) {
			js(w, map[string]any{"data": []any{map[string]any{"id": "s", "attributes": map[string]any{"url": srv.URL + "/files/" + name}}}})
		}
	}
	mux.HandleFunc("/v1/analyticsReportInstances/iEng/segments", seg("eng"))
	mux.HandleFunc("/v1/analyticsReportInstances/iDl/segments", seg("dl"))
	mux.HandleFunc("/v1/analyticsReportInstances/iOld/segments", func(w http.ResponseWriter, r *http.Request) {
		t.Error("instance processed before the window was read")
	})
	mux.HandleFunc("/files/", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			t.Error("API token sent to the pre-signed segment URL")
		}
		switch r.URL.Path {
		case "/files/eng":
			w.Write(gz(t, engagementCSV))
		case "/files/dl":
			io.WriteString(w, downloadsTSV)
		}
	})
	srv = httptest.NewServer(mux)
	c := New("KEY", "ISS", testKey(t))
	c.BaseURL = srv.URL
	return c, srv
}

func TestSalesAndAnalytics(t *testing.T) {
	c, srv := reportServer(t)
	defer srv.Close()
	ctx := context.Background()

	sku, name, err := c.AppSKU(ctx, "111")
	if err != nil || sku != "lexify" || name != "Lexify" {
		t.Fatalf("AppSKU = %q %q %v", sku, name, err)
	}
	rows, ok, err := c.SalesDay(ctx, "8800", "2026-10-12")
	if err != nil || !ok {
		t.Fatalf("SalesDay: ok=%v err=%v", ok, err)
	}
	if _, ok, err := c.SalesDay(ctx, "8800", "2026-10-11"); ok || err != nil {
		t.Fatalf("404 day: ok=%v err=%v, want no data and no error", ok, err)
	}
	m := &DayMetrics{AppID: "111", Date: "2026-10-12"}
	AddSales(m, rows, map[string]string{"lexify": "111"})
	if *m.Downloads != 12 || *m.Redownloads != 2 || *m.Updates != 30 || *m.IAPUnits != 2 {
		t.Errorf("sales units = %d %d %d %d", *m.Downloads, *m.Redownloads, *m.Updates, *m.IAPUnits)
	}
	if m.Proceeds["EUR"] != 4.2 || len(m.Proceeds) != 1 {
		t.Errorf("proceeds = %v, want EUR 4.2 (3 sold, 1 refunded)", m.Proceeds)
	}

	byDate := map[string]*DayMetrics{"2026-10-12": m}
	if err := c.Analytics(ctx, "111", "2026-10-06", "2026-10-12", byDate); err != nil {
		t.Fatal(err)
	}
	if len(byDate) != 1 {
		t.Errorf("days = %d, want only 2026-10-12 (2026-10-01 is outside the window)", len(byDate))
	}
	if *m.Impressions != 120 || *m.ImpressionsUnique != 100 || *m.PageViews != 20 || *m.PageViewsUnique != 15 {
		t.Errorf("engagement = %d %d %d %d", *m.Impressions, *m.ImpressionsUnique, *m.PageViews, *m.PageViewsUnique)
	}
	if *m.FirstTimeDownloads != 6 || *m.AnalyticsRedl != 3 || m.Conversion == nil || *m.Conversion != 0.09 {
		t.Errorf("downloads = %d %d conversion %v", *m.FirstTimeDownloads, *m.AnalyticsRedl, m.Conversion)
	}
	if m.Sessions != nil || m.Crashes != nil {
		t.Error("sessions/crashes set although their reports were not generated")
	}
}

func TestNoAnalyticsRequest(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"data":[]}`)
	}))
	defer srv.Close()
	c := New("KEY", "ISS", testKey(t))
	c.BaseURL = srv.URL
	if err := c.Analytics(context.Background(), "111", "2026-10-06", "2026-10-12", map[string]*DayMetrics{}); err != ErrNoAnalyticsRequest {
		t.Fatalf("err = %v", err)
	}
}

func TestSalesKind(t *testing.T) {
	for pt, want := range map[string]string{"1": "download", "1F": "download", "1T": "download", "1-B": "download", "F1": "download",
		"3": "redownload", "3F": "redownload", "7": "update", "7T": "update", "F7": "update",
		"IA1": "iap", "IAY": "iap", "IA9-M": "iap", "FI1": "iap", "XYZ": ""} {
		if got := salesKind(pt); got != want {
			t.Errorf("%s = %q, want %q", pt, got, want)
		}
	}
}
