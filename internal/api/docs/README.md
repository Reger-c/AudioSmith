# Vendored API reference viewer

`rapidoc-min.js` is [RapiDoc](https://github.com/rapi-doc/RapiDoc) 10.1.0
(MIT, `RAPIDOC-LICENSE.txt`), unmodified, from the npm package `rapidoc`
(`dist/rapidoc-min.js`; tarball integrity
`sha512-yIlpC2/B3xlxf7Iivea3Db8QtHqFps1bkl7K8VVrEMbBKoh2vu7R8dYmiBI9tiSavXP/2japlcSXr4EUYHW1mA==`).

`index.html` loads it from the API's own origin and points it at
`/openapi.json`. The page's Content-Security-Policy allows only the
API's origin, so the viewer works offline (its web fonts fall back to
the system's).

To update: download the new tarball, check its integrity against the
npm registry, replace `rapidoc-min.js` and its licence, and update this
file.
