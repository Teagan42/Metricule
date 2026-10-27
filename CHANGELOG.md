# Changelog

## [0.3.0](https://github.com/Teagan42/Metricule/compare/v0.2.0...v0.3.0) (2026-10-27)


### Features

* **collect:** attribute retries to the retry trigger ([#17](https://github.com/Teagan42/Metricule/issues/17)) ([b74d271](https://github.com/Teagan42/Metricule/commit/b74d27109fbad5dde4fb501e56c3726dfa353a0a))
* **runtime:** wire collection to the exporter ([#16](https://github.com/Teagan42/Metricule/issues/16)) ([65ed9bf](https://github.com/Teagan42/Metricule/commit/65ed9bf07def14a68596a2af222a191c7265c464))

## [0.2.0](https://github.com/Teagan42/Metricule/compare/v0.1.0...v0.2.0) (2026-10-22)


### Features

* **postgres:** add postgresql source adapter ([#11](https://github.com/Teagan42/Metricule/issues/11)) ([6705028](https://github.com/Teagan42/Metricule/commit/67050286ae6161fe2b81bffb4d65098b964342dc))
* **source:** add adapter error classification and diagnostics contracts ([#13](https://github.com/Teagan42/Metricule/issues/13)) ([91c0697](https://github.com/Teagan42/Metricule/commit/91c069765f33443f262579d97cb5c2d18142510f))


### Documentation

* record v0.1 limitations ([#14](https://github.com/Teagan42/Metricule/issues/14)) ([9f01095](https://github.com/Teagan42/Metricule/commit/9f0109531d6f441081d8bf9f4fcb67c06aba7c21))

## 0.1.0 (2026-10-22)


### Features

* add repository scaffold, shared contracts, and configuration ([#1](https://github.com/Teagan42/Metricule/issues/1)) ([15c8efe](https://github.com/Teagan42/Metricule/commit/15c8efe86f2bdce1b161445bd97c84f2ac4aefe9))
* **collect:** add collection scheduler and snapshot store ([#8](https://github.com/Teagan42/Metricule/issues/8)) ([7da61aa](https://github.com/Teagan42/Metricule/commit/7da61aa2e916d1ac42a7a745419fab51e5f866e7))
* **httpjson:** add http json source adapter ([#5](https://github.com/Teagan42/Metricule/issues/5)) ([32f4cfa](https://github.com/Teagan42/Metricule/commit/32f4cfaa25d9b077d9b335717b142e0403618411))
* **mapping:** add mapping engine and prometheus exporter ([#6](https://github.com/Teagan42/Metricule/issues/6)) ([ceb44ef](https://github.com/Teagan42/Metricule/commit/ceb44efad9509a9c9b005f5d5c0a16ad170c2cf9))
* **source:** let document adapters resolve selectors ([#4](https://github.com/Teagan42/Metricule/issues/4)) ([4daab15](https://github.com/Teagan42/Metricule/commit/4daab15cd5514630b79c49c2b198383d6d5876e0))
* **sqlite:** add read-only sqlite source adapter ([#9](https://github.com/Teagan42/Metricule/issues/9)) ([fbdefb7](https://github.com/Teagan42/Metricule/commit/fbdefb7b0f40b0a305908d8acd5144d345f9da18))


### Bug Fixes

* **config:** reject an explicit zero instead of defaulting it ([#10](https://github.com/Teagan42/Metricule/issues/10)) ([42081e9](https://github.com/Teagan42/Metricule/commit/42081e972677bc2ca5f5544b5157305cc609ced8))
* pin the first release to 0.1.0 instead of 1.0.0 ([#2](https://github.com/Teagan42/Metricule/issues/2)) ([a713180](https://github.com/Teagan42/Metricule/commit/a713180153d2df7c26887b92932f740f61604831))


### Documentation

* add binding agent coordination rules ([16858ad](https://github.com/Teagan42/Metricule/commit/16858ad8cc8c68743dd44abb281de7fcd7fd91aa))
* add implementation handoff specification ([28b62da](https://github.com/Teagan42/Metricule/commit/28b62dab3d7da385a4c502a24fd1b9fdb455b70f))
