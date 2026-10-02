# `/st/v1` 계약 golden (#125)

서비스(Go)와 Edge 드라이버(Lua)가 **같은 파일**로 테스트한다.
예전에는 Go 구조체와 Lua 테스트의 손으로 쓴 본문이 따로 놀아서, 필드 이름을 바꿔도 양쪽 테스트가 모두 통과했다.

> **규칙: API를 바꾸면 golden을 갱신하고 양쪽 테스트가 통과해야 한다.**
> Go 쪽만 `-update`로 갱신하고 끝내지 않는다. 갱신된 파일로 Lua 테스트가 깨지면 드라이버도 같이 고친다(또는 API 변경을 되돌린다).

- Go: `service/contract_golden_test.go` (`TestContract*`)
- Lua: `edge/tests/contract_test.lua` (파일은 `edge/tests/helpers.lua`의 `h.fixture`로 읽는다)

## 파일

| 파일 | 내용 | 누가 쓰나 |
|---|---|---|
| `status.full.json` | `GET /st/v1/status` — v1.2.0 블록을 전부 채운 상태(세션, features, audio, media+재생 정보, awake, 배터리, activity, presets, WoL 어댑터 2개, 진행 중 예약, 유예, 업데이트, display, last_command) | Go가 생성 |
| `status.off.json` | `GET /st/v1/status` — 모든 설정이 기본값인 데스크톱(배터리 없음, activity 꺼짐, 재생 정보 동의 없음, 세션 비공개, awake 꺼짐, 예약·최근 명령 없음, WoL 꺼진 어댑터 1개, 화면 꺼짐). 각 블록의 "꺼진" 모양 | Go가 생성 |
| `status.minimal-1.0.json` | v1.2.0 이전 서비스(v1.1.x, 드라이버 1.0.x 시절)가 보내던 모양. `features`와 v1.2 블록이 없다 | **손으로 작성**, 갱신하지 않음 |
| `command.<이름>.json` | 드라이버가 보내는 요청(`request`)과 서비스의 응답(`response.code`, `response.body`) | `request`는 손으로, `response`는 Go가 생성 |
| `command.<이름>.<오류>.json` | 같은 요청이 거절되는 경우(409 `no_user_session`, 403 `media_disabled`, 403 `notify_disabled`) | 위와 같음 |
| `push.<type>.json` | 서비스가 허브 콜백으로 보내는 푸시 본문 전체(`type`, `data`, 그리고 §3.2 status 전체) | Go가 생성 |
| `api-config.get.json` | `GET /api/config` — 데스크톱 앱이 받는 설정. 텔레그램 봇 토큰은 `****` + 끝 4자리로 가리고 `bot_token_set`을 붙인다 | Go가 생성 |

`status.minimal-1.0.json`은 `git show v1.1.2:service/st_api.go`의 `stStatusResponse`에서 옮겼다.
Go 테스트는 이 파일의 모든 키가 지금 status에도 **같은 JSON 타입으로** 남아 있는지 본다(설치된 1.0 드라이버가 깨지지 않게).
Lua 테스트는 이 본문으로 `needs_service` 안내와 v1.0 줄이 그대로 나오는지 본다.

## 각 쪽이 확인하는 것

| | Go | Lua |
|---|---|---|
| status | 실제 핸들러(`stHandler`) 출력 = golden | `state.apply_status` + `features.remember`에 넣고 줄 값을 **리터럴**로 확인 |
| push | 실제 발신 함수(`recordAudioSample`, `emitSessionLock`, `setSchedule`, …) → 버스 → 콜백 서버가 받은 본문 = golden | 원본 바이트를 `push.deliver`(machine_id 라우팅)에 넣고, `push.apply`의 줄 값을 확인 |
| command | `request`를 핸들러의 요청 타입으로 **알 수 없는 키 거부**(`DisallowUnknownFields`) 디코드 → 핸들러 실행 → 응답 = golden | 실제 capability 핸들러가 보내는 본문 = `request` (subscribe의 `driver_version`만 `driver_version.lua`로 바꿔 비교). 오류 응답은 `client.classify` + `features.error_note`로 읽어 본다 |

Lua 쪽 기대값은 일부러 fixture에서 읽지 않고 리터럴로 적는다. 그래야 Go에서 필드 이름이 바뀌어 golden이 갱신되면 Lua가 깨진다.

## 갱신

```sh
# 저장소 루트에서 (Windows: PATH에 mingw64)
go test ./service -run TestContract -count=1 -update
git diff testdata/st-v1          # 바뀐 내용이 의도한 API 변경인지 확인
go test ./service -run TestContract -count=1
cd edge && node tools/lua.js tests/run.lua   # 또는 bun tools/lua.js tests/run.lua
```

- `-update`는 status/push/api-config 파일 전체와 command 파일의 `response`만 다시 쓴다. command의 `request`는 드라이버가 보내는 모양이므로 손으로 고친다.
- 새 `command.*.json`을 추가하면 Go의 `commandCases`와 Lua의 `SENDS`/`REFUSALS`에, 새 `push.*.json`은 Go의 `pushCases`와 Lua의 `PUSHES`에 항목이 있어야 한다(없으면 양쪽 테스트가 실패한다).

## 고정값과 정규화

시각·ID·어댑터·세션은 기존 테스트 seam으로 고정한다(시계 `2026-10-01T21:00:00+09:00`, machine_id `4c4c4544-0042-3510-8052-b4c04f4a3732`).
seam이 없는 값만 **명시적으로** 자리표시자로 바꾼다. 키가 없거나 타입이 다르면 그 자체로 실패한다.

| 필드 | 자리표시자 | 이유 |
|---|---|---|
| `hostname` | `GOLDEN-PC` | `os.Hostname` |
| `uptime_seconds` | `93784` | `DurationSinceBoot` |
| `schedule.remaining_seconds`, `schedule.execute_at` | `1800`, `2026-10-01T21:30:00+09:00` | `setSchedule`이 실제 시계로 타이머를 건다 |
| push `at` | `2026-10-01T21:00:05+09:00` | `emit`이 `time.Now()`를 찍는다 |
| push `data.execute_at`(schedule.created) | `21:30:00` | 위와 같음 |
| subscribe `expires_at` | `2026-10-01T21:10:00+09:00` | 구독 시계 |

## activity 블록

#123의 감시 칸 모양이다. 감시 목록은 1번 칸 `steam.exe`(Steam), 3번 칸 `obs64.exe`(OBS)이고 2·4·5번 칸은 비어 있다(번호가 작을수록 우선, `apps`는 채워진 칸만 칸 순서로). status에서는 Steam만 실행 중(`top` = `steam.exe`), `push.activity.changed.json`에서는 OBS가 켜져 둘 다 실행 중이다(`top`은 그대로 `steam.exe`). 이 푸시의 `data`는 status의 `activity` 블록과 **똑같은** JSON이다.
바꿀 곳은 Go의 `goldenActivityConfig`/`goldenActivity`와 `push.activity.changed.json` 케이스, Lua의 "activity (#123)" 구역뿐이다.
