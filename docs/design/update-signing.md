# 업데이트 매니페스트 서명 (#66)

자동 업데이트는 릴리스에 붙은 **서명된 매니페스트**만 믿는다. 고정된 자산 이름을
내려받아 바로 실행하던 흐름은 없어졌다. 앱은 (1) 매니페스트 서명을 내장 공개키로
검증하고, (2) 매니페스트가 지정한 자산을 받아, (3) SHA-256·크기를 대조한 뒤에야
(4) 기존의 `version` 실행 확인과 UAC 교체 단계로 넘어간다.

## 1. 릴리스가 싣는 파일

| 자산 | 내용 |
|---|---|
| `smartthings-pc-control.exe` | 빌드 산출물(변경 없음) |
| `update.json` | 매니페스트. 들여쓴 JSON, 끝에 개행 하나 |
| `update.json.sig` | `update.json` **바이트 그대로**에 대한 ed25519 서명 64바이트를 표준 base64로 적은 텍스트 |

```json
{
  "version": "v1.0.0",
  "min_version": "v0.3.4",
  "published_at": "2026-09-15T02:10:44Z",
  "assets": [
    {
      "name": "smartthings-pc-control.exe",
      "sha256": "…64 hex…",
      "arch": "amd64",
      "size": 31457280
    }
  ]
}
```

- `version` 은 릴리스 태그와 **정확히** 같아야 한다. 다른 릴리스의 매니페스트를 갖다 붙이는
  재생(replay)을 막기 위해 앱이 `tag_name` 과 비교한다.
- `min_version` (선택) 보다 오래된 설치본은 자동 업데이트를 거부하고 릴리스 페이지로 안내한다.
  업데이터 자체가 호환되지 않게 바뀔 때만 쓴다. 비우면 생략된다.
- `arch` 가 비어 있으면 모든 아키텍처에 매칭된다. 앱은 `runtime.GOARCH` 와 일치하는 항목을
  먼저 고르고, 없으면 `arch` 가 빈 항목을 쓴다.
- `size` 가 0이면 크기 비교는 생략된다(해시는 항상 비교).

타입과 검증 로직은 `internal/release/manifest.go` 에 있다: `Manifest`, `ManifestAsset`,
`VerifyManifest`, `FetchManifest`, `AssetFor`, `Allows`, `(*ManifestAsset).Verify`.

## 2. 만드는 쪽 — CI

`.github/workflows/release.yml` 의 Build 단계 뒤:

```
go run ./internal/tools/signmanifest sign -key env:UPDATE_SIGNING_KEY -version <tag> -arch amd64 -out update.json smartthings-pc-control.exe
go run ./internal/tools/signmanifest verify update.json update.json.sig
```

- `sign` 은 파일마다 sha256/size 를 계산해 `update.json` 을 쓰고, 그 바이트에 서명해
  `update.json.sig` 를 쓴다. `-key env:NAME` 은 시드를 환경 변수에서 읽으므로 명령줄이나
  로그에 나타나지 않는다.
- `verify` 는 `-pub` 을 주지 않으면 **내장 운영 공개키**로 검증한다. 시크릿의 시드와 내장 공개키가
  맞지 않으면(회전 중 한쪽만 바꾼 경우) 여기서 빌드가 실패하므로, 검증 안 되는 릴리스가
  올라가는 일은 없다.
- 세 파일 모두 `softprops/action-gh-release` 의 `files:` 에 올린다.

## 3. 검증하는 쪽 — 앱

`gui/gui.go` `showUpdateDialog` → `showUpdateChoice`, `startSelfUpdate`; `gui/update.go`
`fetchManifest`, `verifyDownloadedHash`.

| 상황 | 동작 |
|---|---|
| dev 빌드(`version` 파싱 불가) | 매니페스트를 보지 않고 릴리스 페이지만 안내(기존 동작) |
| `update.json` 또는 `.sig` 자산이 없음 (`ErrNoManifest`) | `update.unsigned` 문구 + [릴리스 페이지 열기] |
| 서명 불일치·JSON 손상·`version`≠태그 (`ErrBadSignature`) | `update.badsig` + [릴리스 페이지 열기] |
| 다운로드 실패(네트워크) | `update.checkfailed` + 오류 + [릴리스 페이지 열기] |
| `min_version` 보다 오래된 설치본 | `update.minversion` + [릴리스 페이지 열기] |
| 현재 `GOARCH` 자산이 없거나 릴리스에 그 이름의 파일이 없음 | `update.noasset` + [릴리스 페이지 열기] |
| 정상 | [지금 업데이트] → 다운로드 → **무결성 확인**(sha256·size, 불일치 시 파일 삭제 + `update.hashmismatch`) → `version` 실행 확인 → UAC 교체 |

해시 대조는 파일이 **한 번도 실행되기 전**에 이뤄진다. `verifyDownloadedExe` 는 그 다음의
보조 확인으로 남는다. 서명 검증은 `VerifyManifest` 가 JSON 을 해석하기 **전에** 하므로,
검증되지 않은 내용은 어떤 필드도 읽히지 않는다.

## 4. 키 관리

- 알고리즘: ed25519 (표준 라이브러리 `crypto/ed25519`), 시드 32바이트.
- **시드**는 두 곳에만 있다: GitHub Actions 시크릿 `UPDATE_SIGNING_KEY` 와 관리자의 오프라인
  백업. 저장소·이슈·로그·로컬 클론에 절대 남기지 않는다.
- **공개키**는 `internal/release/manifest.go` 의 `PublicKeyBase64` 에 내장된다.
  `TestPublicKey` 가 32바이트로 디코드되는지 확인한다.
- 테스트는 매번 새 키쌍을 만들어 쓴다(`internal/release/manifest_test.go`,
  `internal/tools/signmanifest/main_test.go`). 운영 키로 서명하는 테스트는 없다.

### 회전 절차

1. `go run ./internal/tools/signmanifest genkey` → `seed=…`, `pub=…`
2. 새 시드를 `UPDATE_SIGNING_KEY` 시크릿과 백업에 넣는다. 이전 시드는 파기.
3. `PublicKeyBase64` 를 새 `pub` 으로 바꾸고 이 문서·CHANGELOG 에 기록한 뒤 릴리스한다.
4. 결과: 새 키가 내장된 버전부터는 정상 자동 업데이트. **이전 버전의 앱**은 새 릴리스의
   서명을 검증할 수 없으므로 `update.badsig` 를 보이고 릴리스 페이지로 안내한다 — 즉 회전은
   한 번의 수동 업데이트를 요구한다. 필요하면 회전 릴리스에 `-min-version` 을 함께 올려
   의도를 명시한다.

시드가 유출되었다고 의심되면 즉시 위 절차를 밟는다. 공격자가 유출 키로 만든 매니페스트는
새 공개키가 내장된 앱에서는 통하지 않는다.

## 5. 수동 검증

```
# 릴리스 페이지에서 세 파일을 받은 뒤
go run ./internal/tools/signmanifest verify update.json update.json.sig
sha256sum smartthings-pc-control.exe   # update.json 의 sha256 과 비교
```
