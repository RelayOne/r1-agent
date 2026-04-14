# google-maps

> Google Maps Platform: Maps JavaScript, Geocoding, Directions, Places, Roads. API key restricted by HTTP referrer / app bundle / IP. Billing requires credit card enabled.

<!-- keywords: google maps, places api, geocoding, directions, maps javascript, gmaps -->

**Official docs:** https://developers.google.com/maps/documentation  |  **Verified:** 2026-04-14 (specs stable 2+ years).

## Auth + key restrictions

- REST base: `https://maps.googleapis.com/maps/api/`
- Auth: `?key=AIza...` query param.
- **CRITICAL**: in Google Cloud Console → Credentials, restrict the key to specific APIs AND specific HTTP referrers (for web) / Android bundle IDs / iOS bundle IDs. Unrestricted keys on client-side = bill runaway.

## Maps JavaScript API

```html
<script async src="https://maps.googleapis.com/maps/api/js?key=YOUR_KEY&callback=initMap"></script>
```

```js
function initMap() {
  const map = new google.maps.Map(document.getElementById("map"), {
    center: { lat: 37.77, lng: -122.42 },
    zoom: 12,
  });
  new google.maps.Marker({ position: { lat: 37.77, lng: -122.42 }, map });
}
```

## Geocoding

```
GET /geocode/json?address=Market+St+SF&key=YOUR_KEY
GET /geocode/json?latlng=37.77,-122.42&key=YOUR_KEY   (reverse)
```

Response: `results[0].geometry.location.{lat,lng}`, `formatted_address`, `place_id`, `address_components`.

## Directions

```
GET /directions/json?origin=A&destination=B&mode=driving&key=YOUR_KEY
```

Modes: `driving` | `walking` | `bicycling` | `transit`. `waypoints=A|B|C` for multi-stop. `departure_time=now` enables traffic-aware ETAs.

## Places (autocomplete + details)

```
GET /place/autocomplete/json?input=market+st&key=YOUR_KEY&sessiontoken=UUID
GET /place/details/json?place_id=...&fields=name,geometry,formatted_address&key=YOUR_KEY
```

Use `sessiontoken` for autocomplete: groups related autocomplete + single details call into ONE billing unit instead of per-keystroke billing.

## Rate limits + billing

- 3000 requests/minute default per API; raise in Cloud Console quotas page.
- Each API has its own pricing tier. Maps JavaScript: $7/1000 loads after free quota (28K/month free). Geocoding/Directions ~$5/1000.
- Always enable daily quota caps to prevent surprise bills.

## Common gotchas

- **InvalidValueError** on Maps JS: usually a missing libraries param (e.g. `&libraries=places` for autocomplete).
- **ZERO_RESULTS** on directions: no route between points (water/islands). Check mode.
- **OVER_QUERY_LIMIT**: exceeded per-second burst; exponential backoff.
- **REQUEST_DENIED**: API not enabled or key restriction mismatch.

## Key reference URLs

- Maps JavaScript: https://developers.google.com/maps/documentation/javascript
- Geocoding: https://developers.google.com/maps/documentation/geocoding
- Directions: https://developers.google.com/maps/documentation/directions
- Places: https://developers.google.com/maps/documentation/places
- Quotas/pricing: https://developers.google.com/maps/billing-and-pricing/pricing
