-- Driver version. `config.yml` has no standard `version` field, so this is the
-- single source of truth: it goes into the HTTP User-Agent
-- ("smartthings-pc-control-edge/<version>") that the service records as
-- `hubLastSeen`, and into the `driver_version` field of /st/v1/subscribe (#73).
-- The `edge-vX.Y.Z` release tag must match it (#74).
return "1.0.0"
