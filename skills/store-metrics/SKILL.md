---
name: store-metrics
description: Read-only App Store numbers for Ol1n's apps with the `store-metrics` CLI — downloads, updates, in-app purchases and proceeds from Sales and Trends; impressions, page views, conversion, sessions and crashes from the Analytics Reports. Use when judging how an app is doing or what a promo changed.
metadata: {"openclaw": {"emoji": "📈", "os": ["linux"], "requires": {"bins": ["store-metrics"]}}}
---

# store-metrics

```bash
store-metrics fetch --days 7
store-metrics fetch --days 28 --analytics=false     # sales only
```

- Prints a JSON array, one object per app and day, oldest first, ending yesterday.
- A missing field means there is no data for that day, not zero: Sales and Trends
  appear the next day, Analytics Reports a day or two later. Never report a missing
  number as 0.
- `downloads` are first-time downloads from Sales and Trends; `first_time_downloads`
  is the same thing from Analytics and can differ a little. Prefer `downloads`.
- `conversion` = (first-time downloads + redownloads) / unique impressions, from
  Analytics. Unique counts are summed over sources and devices, so they are a bit
  high and conversion a bit low; compare weeks with each other, not with the number
  App Store Connect shows.
- `proceeds` are developer proceeds per currency, refunds already subtracted.
- `sessions` and `active_devices` only count users who share analytics with developers.
- App Store only. Google Play numbers are not available here.
- Never run `store-metrics request-analytics`: it needs an Admin key that is not on this machine.
