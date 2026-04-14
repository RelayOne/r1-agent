# mapbox

> Mapbox: maps, geocoding, directions, static images. Access tokens (public `pk.*` for client, secret `sk.*` for server admin). Tiles + Vector Styles delivered from `api.mapbox.com`.

<!-- keywords: mapbox, maps, geocoding, directions, routing, mapbox gl, navigation -->

**Official docs:** https://docs.mapbox.com  |  **Verified:** 2026-04-14 (specs stable 2+ years).

## Base URL + auth

- REST base: `https://api.mapbox.com/`
- Auth: `?access_token=pk.eyJ...` query param on every request.
- Public tokens (`pk.*`): client-safe, scope-restricted via dashboard (e.g. URL restrictions, scope=styles:read).
- Secret tokens (`sk.*`): server-only, broader scopes.

## Geocoding (Search Box API — current default)

```
GET /search/searchbox/v1/forward?q=Market+St+SF&access_token=pk...
```

Returns suggestions/features. For reverse geocoding:

```
GET /search/searchbox/v1/reverse?longitude=-122.42&latitude=37.77&access_token=pk...
```

Legacy Geocoding v6 (`/geocoding/v6/...`) still works but Search Box is the 2024+ path.

## Directions

```
GET /directions/v5/mapbox/{profile}/{lng1,lat1;lng2,lat2}?access_token=pk...
  profile: driving | walking | cycling | driving-traffic
```

Response: route geometry (GeoJSON), legs, steps, duration, distance. Up to 25 coordinates per request.

## Static image

```
GET /styles/v1/mapbox/streets-v12/static/-122.42,37.77,14/600x400?access_token=pk...
```

Add overlays in the path (marker, path, geojson).

## Mapbox GL JS (interactive client)

```html
<script src="https://api.mapbox.com/mapbox-gl-js/v3.9.0/mapbox-gl.js"></script>
<link href="https://api.mapbox.com/mapbox-gl-js/v3.9.0/mapbox-gl.css" rel="stylesheet">
```

```js
mapboxgl.accessToken = "pk...";
const map = new mapboxgl.Map({
  container: "map",
  style: "mapbox://styles/mapbox/streets-v12",
  center: [-122.42, 37.77],
  zoom: 12,
});
new mapboxgl.Marker().setLngLat([-122.42, 37.77]).addTo(map);
```

## Billing pitfalls

- Map loads, geocoding requests, directions calls are all separate billing lines.
- Free tier: 50K map loads, 100K geocoding requests/month. Set usage alerts in dashboard.
- Rate-limit to ~600 req/min on geocoding to avoid throttling.

## Key reference URLs

- Search Box API: https://docs.mapbox.com/api/search/search-box/
- Directions: https://docs.mapbox.com/api/navigation/directions/
- Static Images: https://docs.mapbox.com/api/maps/static-images/
- Mapbox GL JS: https://docs.mapbox.com/mapbox-gl-js/api/
