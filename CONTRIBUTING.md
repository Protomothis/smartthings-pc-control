# 기여 안내

## 브랜치

- `main`은 릴리스된 코드다. 릴리스 태그(`vX.Y.Z`, `edge-vX.Y.Z`)는 여기서만 단다.
- 한 릴리스의 작업은 `milestone/vX.Y.Z` 브랜치에 모은다. CI는 `main`과 `milestone/**` 푸시, `main`으로 가는 PR에서 돈다.
- 작업마다 마일스톤 브랜치에서 짧은 브랜치를 딴다(`feat/123-…`, `fix/…`, `edge/…`, `gui/…`, `docs/…`, `test/…`, `ci/…`). 끝나면 `Merge <브랜치> into milestone/vX.Y.Z (#n)`로 합치고 지운다.
- 마일스톤은 사용자 테스트가 끝난 뒤 `main`에 합치고 태그를 단다. rc 태그(`-rc1`)는 시험판으로 나간다.

## 커밋

- 제목은 `<스코프>: <한국어 제목>`, 50자 이하, 마침표 없이 무엇을 하는지 쓴다. 예: `service: 잠기지 않은 사용자 세션을 대상으로 고른다`
- 스코프: `service` `gui` `edge` `docs` `ci` `test` `internal` `useraction`. 둘에 걸치면 `gui/useraction:`처럼 쓴다.
- 본문에는 **왜** 바꾸는지를 쓴다. 실측 결과, 버린 대안, 남은 위험도 여기에 둔다. 관련 이슈는 `(#n)`으로 적는다.
- AI 도구의 도움을 받았으면 본문 끝에 `Co-Authored-By:` 트레일러를 붙인다.
- 사용자에게 보이는 변경은 `CHANGELOG.md`(Windows 앱) 또는 `edge/CHANGELOG.md`(드라이버)의 `[Unreleased]`에 한두 문장으로 적는다. 구현 세부는 커밋 본문에 둔다.

## 테스트

Go 쪽은 Fyne 때문에 CGO와 MinGW-w64 gcc가 필요하다(예: `PATH="/c/msys64/mingw64/bin:$PATH"`).

```bash
gofmt -l .                                         # 비어 있어야 한다
CGO_ENABLED=1 go vet ./... && CGO_ENABLED=1 go test ./...
golangci-lint run ./...                            # 설정은 .golangci.yml
go test ./service -run TestContract -update        # /st/v1 계약을 바꿨을 때 testdata/st-v1 다시 쓰기
CGO_ENABLED=1 go build -ldflags="-s -w -H=windowsgui -X main.Version=dev" -o smartthings-pc-control.exe .
```

계약 golden(`testdata/st-v1`)은 Go와 Lua 테스트가 함께 쓴다. `-update`로 다시 썼으면 Lua 테스트도 돌린다.

```bash
cd edge
bun tools/lua.js tests/run.lua          # Lua 5.3 테스트 (npm install && npm test 도 같다)
bun tools/lua.js tests/syntax.lua       # 전 모듈 컴파일
node tools/gen-profiles.js --check      # 생성된 프로필이 템플릿과 같은지
```

## Edge 드라이버 빌드와 배포

```bash
cd edge
node tools/build.js && smartthings edge:drivers:package build/edge
smartthings edge:channels:assign <driverId> <version> --channel <channelId>
```

패키지는 주석을 뗀 `build/edge/`로 만든다(커밋하지 않는다).
화면을 바꾸면 프로필 버전을, capability 정의를 바꾸면 id를 새로 해야 한다. 규칙은 [`edge/README.md`](edge/README.md)와 [`docs/design/edge-driver.md`](docs/design/edge-driver.md) §6.6에 있다.

## 문서

- 사용자 안내의 정본은 [Wiki](https://github.com/Protomothis/smartthings-pc-control/wiki)다. README는 150줄, `edge/README.md`는 200줄 안으로 둔다.
- `docs/design/`은 결정과 근거만 담고, 릴리스 뒤에는 고치지 않는다(300줄 이하). 진행 메모와 실측 대기는 이슈로 옮긴다.
- SmartThings 플랫폼의 함정은 [`docs/design/edge-platform-notes.md`](docs/design/edge-platform-notes.md) 한 곳에 적는다.
