package appstore

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/lioilsources/promoclown/internal/monitor/cliutil"
)

// Reports need more than the reviews key: Sales and Trends and reading the
// Analytics Reports take the "Sales and Reports" (or Finance, Admin) role, and
// creating the analytics report request once takes Admin.

// DayMetrics is one app's numbers for one day. A nil field means the source
// had no data for that day (report not generated yet, or no access), which is
// not the same as zero.
type DayMetrics struct {
	AppID   string `json:"app_id"`
	AppName string `json:"app_name,omitempty"`
	Date    string `json:"date"`

	// Sales and Trends (SALES SUMMARY DAILY): units by product type.
	Downloads   *int               `json:"downloads,omitempty"`   // first-time, product types 1*, F1
	Redownloads *int               `json:"redownloads,omitempty"` // 3, 3F
	Updates     *int               `json:"updates,omitempty"`     // 7*, F7
	IAPUnits    *int               `json:"iap_units,omitempty"`   // IA*, FI1, refunds negative
	Proceeds    map[string]float64 `json:"proceeds,omitempty"`    // developer proceeds by currency

	// Analytics Reports, Standard level.
	Impressions        *int     `json:"impressions,omitempty"`
	ImpressionsUnique  *int     `json:"impressions_unique,omitempty"`
	PageViews          *int     `json:"page_views,omitempty"`
	PageViewsUnique    *int     `json:"page_views_unique,omitempty"`
	FirstTimeDownloads *int     `json:"first_time_downloads,omitempty"`
	AnalyticsRedl      *int     `json:"analytics_redownloads,omitempty"`
	Conversion         *float64 `json:"conversion,omitempty"` // (first-time + redownloads) / unique impressions
	Sessions           *int     `json:"sessions,omitempty"`
	SessionSeconds     *int     `json:"session_seconds,omitempty"`
	ActiveDevices      *int     `json:"active_devices,omitempty"`
	Crashes            *int     `json:"crashes,omitempty"`
}

// ---------- Sales and Trends ----------

// SalesDay downloads the SALES SUMMARY DAILY report for date (YYYY-MM-DD) and
// sums it per app. Apple answers 404 both when nothing sold that day and when
// the report is not generated yet (it appears the next day); both come back as
// ok=false with no error.
func (c *Client) SalesDay(ctx context.Context, vendor, date string) (rows []SalesRow, ok bool, err error) {
	q := url.Values{
		"filter[frequency]": {"DAILY"}, "filter[reportType]": {"SALES"}, "filter[reportSubType]": {"SUMMARY"},
		"filter[version]": {"1_0"}, "filter[vendorNumber]": {vendor}, "filter[reportDate]": {date},
	}
	resp, err := c.do(ctx, http.MethodGet, c.BaseURL+"/v1/salesReports?"+q.Encode(), nil, "application/a-gzip")
	if err != nil {
		var he *cliutil.HTTPError
		if errors.As(err, &he) && he.Status == http.StatusNotFound {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("sales report %s: %w", date, err)
	}
	defer resp.Body.Close()
	rows, err = parseSales(resp.Body)
	if err != nil {
		return nil, false, fmt.Errorf("sales report %s: %w", date, err)
	}
	return rows, true, nil
}

// SalesRow is the part of a sales report line the metrics use.
type SalesRow struct {
	AppleID, ParentID, SKU, Title, ProductType, Currency string
	Units                                                int
	Proceeds                                             float64 // per unit
}

func parseSales(r io.Reader) ([]SalesRow, error) {
	t, err := readTable(r)
	if err != nil {
		return nil, err
	}
	var out []SalesRow
	for _, rec := range t.rows {
		units, _ := strconv.ParseFloat(t.get(rec, "Units"), 64)
		proceeds, _ := strconv.ParseFloat(t.get(rec, "Developer Proceeds"), 64)
		out = append(out, SalesRow{
			AppleID: t.get(rec, "Apple Identifier"), ParentID: t.get(rec, "Parent Identifier"), SKU: t.get(rec, "SKU"),
			Title: t.get(rec, "Title"), ProductType: t.get(rec, "Product Type Identifier"),
			Currency: t.get(rec, "Currency of Proceeds"), Units: int(units), Proceeds: proceeds,
		})
	}
	return out, nil
}

// salesKind sorts a product type identifier into the metric it counts toward.
// developer.apple.com/help/app-store-connect/reference/reporting/product-type-identifiers
func salesKind(pt string) string {
	pt = strings.ToUpper(strings.TrimSpace(pt))
	switch {
	case pt == "3" || pt == "3F":
		return "redownload"
	case strings.HasPrefix(pt, "7") || pt == "F7":
		return "update"
	case strings.HasPrefix(pt, "IA") || pt == "FI1":
		return "iap"
	case strings.HasPrefix(pt, "1") || strings.HasPrefix(pt, "F1"):
		return "download"
	}
	return ""
}

// AddSales folds one day's sales rows into m. skus maps an app SKU to its
// Apple ID, because in-app purchase rows only name their parent app by SKU.
func AddSales(m *DayMetrics, rows []SalesRow, skus map[string]string) {
	zero := func(p **int) {
		if *p == nil {
			*p = new(int)
		}
	}
	zero(&m.Downloads)
	zero(&m.Redownloads)
	zero(&m.Updates)
	zero(&m.IAPUnits)
	for _, r := range rows {
		app := r.AppleID
		kind := salesKind(r.ProductType)
		if kind == "iap" {
			app = skus[r.ParentID]
		}
		if app != m.AppID || kind == "" {
			continue
		}
		if m.AppName == "" && kind != "iap" {
			m.AppName = r.Title
		}
		switch kind {
		case "download":
			*m.Downloads += r.Units
		case "redownload":
			*m.Redownloads += r.Units
		case "update":
			*m.Updates += r.Units
		case "iap":
			*m.IAPUnits += r.Units
		}
		if r.Proceeds != 0 && r.Units != 0 {
			if m.Proceeds == nil {
				m.Proceeds = map[string]float64{}
			}
			m.Proceeds[r.Currency] = round2(m.Proceeds[r.Currency] + r.Proceeds*float64(r.Units))
		}
	}
}

func round2(v float64) float64 { return float64(int64(v*100+copysign(0.5, v))) / 100 }

func copysign(a, b float64) float64 {
	if b < 0 {
		return -a
	}
	return a
}

// AppSKU reads the app's SKU and name.
func (c *Client) AppSKU(ctx context.Context, appID string) (sku, name string, err error) {
	var body struct {
		Data struct {
			Attributes struct {
				SKU  string `json:"sku"`
				Name string `json:"name"`
			} `json:"attributes"`
		} `json:"data"`
	}
	err = c.get(ctx, c.BaseURL+"/v1/apps/"+url.PathEscape(appID)+"?fields[apps]=sku,name", &body)
	return body.Data.Attributes.SKU, body.Data.Attributes.Name, err
}

// ---------- Analytics Reports ----------

// The Standard reports the metrics come from. Names carry a level suffix
// ("… Standard" / "… Detailed"); App Crashes has no levels.
var analyticsReports = []string{
	"App Store Discovery and Engagement",
	"App Store Downloads",
	"App Sessions",
	"App Crashes",
}

// ErrNoAnalyticsRequest means nobody has asked Apple to generate analytics
// reports for the app yet (store-metrics request-analytics, Admin key).
var ErrNoAnalyticsRequest = errors.New("no active ONGOING analytics report request (run store-metrics request-analytics with an Admin key once)")

type jsonAPIList struct {
	Data []struct {
		ID         string          `json:"id"`
		Attributes json.RawMessage `json:"attributes"`
	} `json:"data"`
	Links struct {
		Next string `json:"next"`
	} `json:"links"`
}

func (c *Client) list(ctx context.Context, u string, each func(id string, attrs json.RawMessage) error) error {
	for page := 0; u != "" && page < maxPages; page++ {
		var body jsonAPIList
		if err := c.get(ctx, u, &body); err != nil {
			return err
		}
		for _, d := range body.Data {
			if err := each(d.ID, d.Attributes); err != nil {
				return err
			}
		}
		u = body.Links.Next
		if u != "" && !strings.HasPrefix(u, c.BaseURL+"/") {
			return fmt.Errorf("refusing to follow next link off %s", c.BaseURL)
		}
	}
	return nil
}

// ongoingRequest returns the app's active ONGOING report request id, or "".
func (c *Client) ongoingRequest(ctx context.Context, appID string) (string, error) {
	var id string
	err := c.list(ctx, c.BaseURL+"/v1/apps/"+url.PathEscape(appID)+"/analyticsReportRequests?filter[accessType]=ONGOING",
		func(rid string, a json.RawMessage) error {
			var attrs struct {
				StoppedDueToInactivity bool `json:"stoppedDueToInactivity"`
			}
			_ = json.Unmarshal(a, &attrs)
			if !attrs.StoppedDueToInactivity && id == "" {
				id = rid
			}
			return nil
		})
	return id, err
}

// RequestAnalytics creates the ONGOING report request for an app unless an
// active one exists. Needs an Admin key; Apple starts generating in 1–2 days.
func (c *Client) RequestAnalytics(ctx context.Context, appID string) (id string, created bool, err error) {
	if id, err = c.ongoingRequest(ctx, appID); err != nil || id != "" {
		return id, false, err
	}
	body := map[string]any{"data": map[string]any{
		"type":          "analyticsReportRequests",
		"attributes":    map[string]any{"accessType": "ONGOING"},
		"relationships": map[string]any{"app": map[string]any{"data": map[string]any{"type": "apps", "id": appID}}},
	}}
	b, _ := json.Marshal(body)
	resp, err := c.do(ctx, http.MethodPost, c.BaseURL+"/v1/analyticsReportRequests", b, "application/json")
	if err != nil {
		return "", false, err
	}
	defer resp.Body.Close()
	var out struct {
		Data struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", false, err
	}
	return out.Data.ID, true, nil
}

// Analytics adds the analytics numbers for days [from, to] to byDate (keyed by
// YYYY-MM-DD; missing days are created). A day that sits in several daily
// instances (Apple re-issues corrected data) takes the latest processing date.
func (c *Client) Analytics(ctx context.Context, appID string, from, to string, byDate map[string]*DayMetrics) error {
	reqID, err := c.ongoingRequest(ctx, appID)
	if err != nil {
		return err
	}
	if reqID == "" {
		return ErrNoAnalyticsRequest
	}
	reports := map[string]string{} // base name → report id
	err = c.list(ctx, c.BaseURL+"/v1/analyticsReportRequests/"+url.PathEscape(reqID)+"/reports?limit=200",
		func(id string, a json.RawMessage) error {
			var attrs struct {
				Name string `json:"name"`
			}
			_ = json.Unmarshal(a, &attrs)
			for _, base := range analyticsReports {
				if attrs.Name == base || attrs.Name == base+" Standard" {
					reports[base] = id
				}
			}
			return nil
		})
	if err != nil {
		return err
	}
	var errs []error
	for _, base := range analyticsReports {
		id, ok := reports[base]
		if !ok {
			continue // not generated yet for a fresh request
		}
		days, err := c.reportDays(ctx, id, from, to)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", base, err))
			continue
		}
		for date, t := range days {
			m := byDate[date]
			if m == nil {
				m = &DayMetrics{AppID: appID, Date: date}
				byDate[date] = m
			}
			applyAnalytics(m, base, t)
		}
	}
	for _, m := range byDate {
		if m.ImpressionsUnique != nil && *m.ImpressionsUnique > 0 && m.FirstTimeDownloads != nil {
			d := *m.FirstTimeDownloads
			if m.AnalyticsRedl != nil {
				d += *m.AnalyticsRedl
			}
			v := float64(int(float64(d)/float64(*m.ImpressionsUnique)*10000+0.5)) / 10000
			m.Conversion = &v
		}
	}
	return errors.Join(errs...)
}

// sums of one report for one day: counts keyed by "column" or "column|dimension value"
type daySums map[string]int

// reportDays reads the daily instances of a report processed from `from`
// onward and returns per-day sums restricted to [from, to].
func (c *Client) reportDays(ctx context.Context, reportID, from, to string) (map[string]daySums, error) {
	type inst struct{ id, processed string }
	var insts []inst
	err := c.list(ctx, c.BaseURL+"/v1/analyticsReports/"+url.PathEscape(reportID)+"/instances?filter[granularity]=DAILY&limit=200",
		func(id string, a json.RawMessage) error {
			var attrs struct {
				ProcessingDate string `json:"processingDate"`
			}
			_ = json.Unmarshal(a, &attrs)
			if attrs.ProcessingDate >= from {
				insts = append(insts, inst{id, attrs.ProcessingDate})
			}
			return nil
		})
	if err != nil {
		return nil, err
	}
	sort.Slice(insts, func(i, j int) bool { return insts[i].processed < insts[j].processed })
	out := map[string]daySums{}
	for _, in := range insts {
		days := map[string]daySums{}
		err := c.list(ctx, c.BaseURL+"/v1/analyticsReportInstances/"+url.PathEscape(in.id)+"/segments?limit=200",
			func(_ string, a json.RawMessage) error {
				var attrs struct {
					URL string `json:"url"`
				}
				_ = json.Unmarshal(a, &attrs)
				if attrs.URL == "" {
					return nil
				}
				return c.readSegment(ctx, attrs.URL, from, to, days)
			})
		if err != nil {
			return nil, err
		}
		for d, s := range days { // later processing date replaces earlier data for the day
			out[d] = s
		}
	}
	return out, nil
}

// readSegment downloads one segment file and adds its rows to days. The URL
// is pre-signed and short-lived; it must not carry the API token.
func (c *Client) readSegment(ctx context.Context, u, from, to string, days map[string]daySums) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if err := cliutil.CheckResponse(resp); err != nil {
		return fmt.Errorf("segment: %w", err)
	}
	t, err := readTable(resp.Body)
	if err != nil {
		return fmt.Errorf("segment: %w", err)
	}
	for _, rec := range t.rows {
		date := t.get(rec, "Date")
		if date < from || date > to {
			continue
		}
		s := days[date]
		if s == nil {
			s = daySums{}
			days[date] = s
		}
		s.add(t, rec)
	}
	return nil
}

// add sums every numeric column of the row, keyed per app, overall and per
// the value of the dimension column the metric needs ("Event", "Download Type").
func (s daySums) add(t *table, rec []string) {
	app := t.get(rec, "App Apple Identifier")
	dims := []string{"", "Event=" + strings.ToLower(t.get(rec, "Event")), "Download Type=" + strings.ToLower(t.get(rec, "Download Type"))}
	for _, col := range []string{"Counts", "Unique Counts", "Sessions", "Total Session Duration", "Unique Devices", "Crashes"} {
		i, ok := t.idx[strings.ToLower(col)]
		if !ok || i >= len(rec) {
			continue
		}
		v, err := strconv.ParseFloat(strings.TrimSpace(rec[i]), 64)
		if err != nil {
			continue
		}
		for _, d := range dims {
			s[app+"|"+col+"|"+d] += int(v)
		}
	}
}

func applyAnalytics(m *DayMetrics, report string, s daySums) {
	get := func(col, dim string) *int {
		v, ok := s[m.AppID+"|"+col+"|"+dim]
		if !ok {
			v = 0 // report present for the day, app had no rows: a real zero
		}
		return &v
	}
	switch report {
	case "App Store Discovery and Engagement":
		m.Impressions = get("Counts", "Event=impression")
		m.ImpressionsUnique = get("Unique Counts", "Event=impression")
		m.PageViews = get("Counts", "Event=page view")
		m.PageViewsUnique = get("Unique Counts", "Event=page view")
	case "App Store Downloads":
		m.FirstTimeDownloads = get("Counts", "Download Type=first-time download")
		m.AnalyticsRedl = get("Counts", "Download Type=redownload")
	case "App Sessions":
		m.Sessions = get("Sessions", "")
		m.SessionSeconds = get("Total Session Duration", "")
		m.ActiveDevices = get("Unique Devices", "")
	case "App Crashes":
		m.Crashes = get("Crashes", "")
	}
}

// ---------- shared ----------

// table is a header-indexed TSV/CSV, gunzipped if needed.
type table struct {
	idx  map[string]int
	rows [][]string
}

func (t *table) get(rec []string, col string) string {
	i, ok := t.idx[strings.ToLower(col)]
	if !ok || i >= len(rec) {
		return ""
	}
	return strings.TrimSpace(rec[i])
}

func readTable(r io.Reader) (*table, error) {
	br := bufio.NewReader(r)
	if magic, _ := br.Peek(2); len(magic) == 2 && magic[0] == 0x1f && magic[1] == 0x8b {
		zr, err := gzip.NewReader(br)
		if err != nil {
			return nil, err
		}
		defer zr.Close()
		br = bufio.NewReader(zr)
	}
	data, err := io.ReadAll(io.LimitReader(br, 512<<20))
	if err != nil {
		return nil, err
	}
	header, _, _ := bytes.Cut(data, []byte("\n"))
	cr := csv.NewReader(bytes.NewReader(data))
	cr.Comma = ','
	if bytes.Contains(header, []byte("\t")) {
		cr.Comma = '\t'
	}
	cr.LazyQuotes = true
	cr.FieldsPerRecord = -1
	recs, err := cr.ReadAll()
	if err != nil {
		return nil, err
	}
	t := &table{idx: map[string]int{}}
	if len(recs) == 0 {
		return t, nil
	}
	for i, h := range recs[0] {
		t.idx[strings.ToLower(strings.TrimSpace(strings.TrimPrefix(h, "\ufeff")))] = i
	}
	t.rows = recs[1:]
	return t, nil
}

func (c *Client) do(ctx context.Context, method, u string, body []byte, accept string) (*http.Response, error) {
	tok, err := c.Token()
	if err != nil {
		return nil, err
	}
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, rd)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Accept", accept)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	if err := cliutil.CheckResponse(resp); err != nil {
		resp.Body.Close()
		return nil, err
	}
	return resp, nil
}

// Dates lists the days [today-days, today-1] as YYYY-MM-DD in UTC; Apple's
// reports are dated in Pacific time but a day either way only shifts the window.
func Dates(now time.Time, days int) []string {
	var out []string
	for i := days; i >= 1; i-- {
		out = append(out, now.UTC().AddDate(0, 0, -i).Format("2006-01-02"))
	}
	return out
}
