# 설계: 미디어(재생 정보 포함)·알림·프리셋·활동·잠들지 않기·배터리 (v1.2.0 / Edge 1.1.0)

전원만 다루던 PC Control에 일곱 가지를 한 번에 더한다: **볼륨·음소거**, **미디어 제어**, **PC에 알림 띄우기**,
**프리셋 실행**, **실행 중 앱 감지**, **잠들지 않기**, **노트북 배터리**. 모두 SmartThings 앱·루틴·텔레그램에서 쓸 수 있어야 한다.

## 1. 목표와 범위

| 기능 | SmartThings | 텔레그램 | 비고 |
|---|---|---|---|
| 볼륨 | 슬라이더, 올리기/내리기, 현재 값 표시 | `/vol 30`, `/vol +10` | 기본 재생 장치 기준 |
| 음소거 | 켜기/끄기, 현재 상태 | `/mute`, `/unmute` | |
| 미디어 | 재생/일시정지/정지, 다음/이전 곡 | `/play` `/pause` `/next` `/prev` | 미디어 키 전송. 재생 상태는 1차에서 보고하지 않음 |
| PC 알림 | 루틴 동작 "PC에 알림"(문구) | `/say 문구` | 토스트 기본, 소리내어 읽기는 설정으로 켬 |
| 프리셋 실행 | 목록에서 슬롯 선택, 슬롯 이름 목록 줄 | `/presets`, `/run 이름` | PC 앱에 등록한 것만 실행 (§10) |
| 앱 감지 | 활동 줄 "게임 중 · Steam", 루틴 조건 "활동이 게임" | `/status`에 포함 | 옵트인, 감시 목록의 라벨만 보고 (§11) |
| 잠들지 않기 | 별도 컴포넌트의 스위치 | `/awake [분|off]` | 자동 절전만 막음, 시간 제한 (§12) |
| 노트북 배터리 | 잔량·전원 공급원 (배터리 있는 PC만) | `/status`에 포함 | 표준 `battery`·`powerSource` (§13) |

**보류:** CPU·GPU 온도(믿을 만한 공개 API 없음), 화면 밝기(외장 모니터 DDC/CI 불안정).

## 2. 아키텍처: 세션 0과 사용자 세션

서비스는 세션 0에서 돈다. 볼륨(Core Audio 기본 장치), 미디어 키(SendInput), 토스트, 음성(SAPI)은
**로그인한 사용자의 세션에서만** 의미가 있다.

```
허브 ──/st/v1──▶ 서비스(세션 0) ──runInUserSession──▶ exe user-action …(사용자 세션)
                     ▲                                       │ stdout: JSON 결과·현재 상태
                     └───────────────────────────────────────┘
트레이 앱(사용자 세션) ──/api/session/heartbeat(30초, audio 포함)──▶ 서비스
```

- **실행 경로:** 서비스가 이미 쓰는 `runInUserSession`(CreateProcessAsUser, 출력 수집)으로 같은 exe의
  하위 명령 `user-action`을 사용자 세션에서 실행한다. 트레이 앱이 꺼져 있어도 동작한다.
  - `user-action audio get` / `audio set <0-100>` / `audio step <±n>` / `audio mute <on|off|toggle>`
  - `user-action media <playpause|play|pause|stop|next|prev>`
  - `user-action notify --title … --text … [--speak]`
  - 결과는 한 줄 JSON(`{"ok":true,"audio":{"volume":30,"muted":false,"device":"스피커"}}`)으로 stdout에 쓴다.
- **상태 보고:** 볼륨은 사용자가 키보드로도 바꾸므로, 트레이 하트비트(30초)에 `audio` 블록을 실어 보낸다.
  명령 직후에는 `user-action`의 결과로 즉시 갱신하고 푸시로 허브에 알린다.
- **사용자 세션이 없을 때:** 로그인한 사용자가 없으면 명령을 `409 no_user_session`으로 거절하고
  status의 `audio.available=false`로 알린다. 드라이버는 요약 줄에 "사용자 없음"을 쓴다.
- **구현 선택:** Core Audio는 COM(`IMMDeviceEnumerator` → `IAudioEndpointVolume`)을 Go에서 직접 부른다
  (go-ole 계열). PowerShell + C# Add-Type는 호출마다 1초 가까이 걸려 슬라이더에 부적합하다.
  미디어 키는 `SendInput`(VK_MEDIA_*), 토스트는 기존 go-toast, 음성은 SAPI `SpVoice`.

### user-action 확정 문법 (#103)

위 목록에서 `notify --voice`와 `preset`이 늘었고 오류 모양을 정했다. 구현은 `useraction/`.

- `audio get` · `audio set <0-100>` · `audio step <-100..100>`(`+5`·`-5`·`5`) · `audio mute <on|off|toggle>`
- `media <playpause|play|pause|stop|next|prev>`
- `notify --title <t> --text <t> [--speak] [--voice <name>]` — 값은 다음 인자를 그대로 받는다(`--`로 시작해도 문구).
  `--text` 1–200자, 제목 100자, 제어 문자는 거절(서비스가 먼저 지운다).
- `preset --type <program|url|script> --path <p> [--arg <a>]...` — `url`은 http/https만·`--arg` 없음,
  `script`는 `.ps1`/`.bat`/`.cmd`만, `--arg` 최대 32개.
- 출력은 stdout 한 줄: `{"ok":true,...}`(종료 0) 또는 `{"ok":false,"error":"<code>","message":"..."}`(종료 1).
  코드는 `bad_args` · `unsupported`(처리기가 없거나 이 PC에서 못 함) · `failed`.
- 기능 이슈는 `useraction.Register(action, handler)`로 처리기를 붙인다. 붙기 전에는 `unsupported`.
- 서비스는 `runUserAction`으로 부른다: 같은 파서로 먼저 검사(잘못된 인자는 프로세스를 띄우지 않음),
  3초 제한(넘으면 자식 종료), 출력의 **마지막 비지 않은 줄**을 JSON으로 읽는다. 결과에 `audio`가 있으면 저장값을 갱신한다.
- 하트비트 본문의 `idle_seconds`와 `audio`는 각각 선택이다. `audio`가 범위를 벗어나면 본문 전체를 400으로 거절한다.

## 3. 서비스 API 추가 (`/st/v1`, protocol 1 유지)

- **status**에 추가:
  - `features: ["audio","media","notify"]` — 드라이버가 기능 유무를 판단한다(옛 서비스는 키 없음).
  - `audio: { available, volume, muted, device, updated_at }` — 마지막 하트비트나 명령 결과.
- **command**(`POST /st/v1/command`)에 명령 추가. 기존 `command`/`mode`/`minutes` 옆에 `value`를 둔다.
  - `volume`(value 0–100), `volumeup`/`volumedown`(value 기본 5), `mute`/`unmute`
  - `play`/`pause`/`playpause`/`stop`/`next`/`prev`
  - 유예·예약과 무관하게 **즉시 실행**. 텔레그램 알림은 기본 끔(볼륨 조절마다 알림이 오면 소음).
- **notify**(`POST /st/v1/notify`): `{ "title"?: string, "text": string, "speak"?: bool }`.
  - `text` 1–200자, 제목 기본값은 "SmartThings". 제어 문자 제거.
  - 출처 IP별 분당 10회. 설정에서 끄면 `403 notify_disabled`.
- 푸시(`/pc/evt`)에 `audio.changed` 이벤트를 더한다.

## 4. 설정 (config.json)

```json
"media": { "enabled": true },
"notify_pc": { "enabled": true, "speak": false, "voice": "" }
```

- `media.enabled`: 볼륨·미디어 명령 허용(기본 켬).
- `notify_pc.enabled`: 외부에서 PC 화면에 알림을 띄우는 것 허용(기본 켬). `speak`: 소리내어 읽기(기본 끔).
  `voice`: SAPI 음성 이름, 비우면 시스템 기본(한국어 음성은 Windows 언어 팩에 따라 없을 수 있음).
- 데스크톱 앱: 새 **미디어·알림** 섹션(설정 탭 또는 네트워크 탭 SmartThings 섹션 아래), 테스트 버튼 포함.

## 5. Edge 드라이버 (1.1.0)

| 기능 | capability | 종류 | 화면 |
|---|---|---|---|
| 볼륨 | `audioVolume` | 표준 | 상세 화면 슬라이더 |
| 음소거 | `audioMute` | 표준 | 토글 |
| 미디어 | `mediaPlayback` + `mediaTrackControl` | 표준 | 재생/일시정지, 이전/다음 버튼 |
| PC 알림 | 표준 `notification` 또는 `speechSynthesis` 실측 → 루틴에 안 나오면 커스텀 `pcNotify` | 자동화 전용 | 루틴 동작 "PC에 알림 띄우기"(문구 입력) |

- 표준 capability는 정의 캐시 문제가 없고 앱 기본 UI를 그대로 쓴다. SmartThings에 연결된 음성 비서가
  볼륨을 인식하는지는 Dev 채널에서 확인한다.
- `mediaPlayback.playbackStatus`는 1차에서 보고하지 않는다. 버튼만 쓰고 상태는 `stopped`로 고정하지 않도록
  `supportedPlaybackCommands`만 방출하고 재생 상태 줄이 비는지 실측한다(비면 "알 수 없음" 대체값을 검토).
- **프로필:** capability가 늘어나므로 `pc.v2`로 올린다. 아이콘 변형 10개(`pc-<style>.v2`)를 함께 올리고
  `pc*.v1`은 `KNOWN`에 넣어 자동 이전한다(§6.6 규칙). 이전 직후 `repaint_soon`.
- **옛 서비스(features 없음):** 볼륨 줄은 비활성 안내("서비스 v1.2.0 필요")를 요약에 쓰고 명령은 보내지 않는다.
- **사용자 세션 없음:** `audio.available=false`면 명령을 보내지 않고 요약에 "사용자 없음".

## 6. 텔레그램

`/vol [0-100|+n|-n]`, `/mute`, `/unmute`, `/play`, `/pause`, `/next`, `/prev`, `/say 문구`.
`/vol`만 치면 현재 볼륨과 음소거 상태를 답한다. `/say`는 `notify_pc.enabled`를 따른다.

## 7. 보안

- 기존과 같이 시크릿(`X-PC-Secret`)과 `allowed_hubs`가 모든 `/st/v1`를 막는다.
- 알림 문구는 길이 제한·제어 문자 제거·출처별 속도 제한. 토스트는 문구만 보여 주고 링크나 동작 버튼을 달지 않는다.
- 미디어 키·볼륨은 파괴적이지 않지만 설정으로 끌 수 있게 한다.
- `user-action`은 서비스가 사용자 세션에 띄우는 내부 하위 명령이며, 인자를 그대로 셸에 넘기지 않는다.

## 8. 테스트와 실측

- 서비스: 명령 파싱·범위 검사·속도 제한·세션 없음 응답의 단위 테스트, `user-action` 출력 파서 테스트.
- GUI: 섹션 문구·설정 저장 테스트, 테스트 버튼.
- 드라이버: 표준 capability 방출·명령 매핑·옛 서비스/세션 없음 분기 테스트, 프로필 변형 동기 테스트.
- **Dev 채널 실측 목록**
  1. 볼륨 슬라이더·음소거 토글·미디어 버튼이 상세 화면에 어떻게 그려지는가(대시보드 포함)
  2. 표준 `notification`/`speechSynthesis`가 루틴 동작에 나오는가, 문구 입력이 되는가
  3. 재생 상태를 보고하지 않을 때 미디어 줄이 비는가
  4. 음성 비서에서 볼륨 명령이 먹는가(선택)

## 9. 순서

1. 서비스 기반: `user-action` 하위 명령과 결과 수집, 하트비트 `audio` 블록 (#103)
2. 서비스 기능(병렬): 오디오 (#104), 미디어 (#105), PC 알림 (#106), 프리셋 (#109), 앱 감지 (#110), 잠들지 않기 (#111), 배터리 (#112)
3. 데스크톱 앱: 미디어·알림 섹션(#106), 프리셋 편집기(#109), 감시 목록(#110), 잠들지 않기 토글(#111)
4. 드라이버: 프로필 생성기와 pc.v2(#107), PC 알림(#108), 프리셋(#113), 활동(#114), 잠들지 않기 컴포넌트(#115), 배터리 변형(#116)
5. Dev 채널 실측 → 수정 → v1.2.0 릴리스 → 공개 채널 edge-v1.1.0

드라이버는 서비스 v1.2.0이 먼저 나가야 의미가 있으므로 **앱 릴리스 → 드라이버 공개** 순서로 낸다.

## 10. 프리셋 실행

PC 앱에 미리 등록한 동작만 원격에서 고를 수 있다. 원격은 **슬롯 번호만** 보내고, 무엇을 실행할지는 PC에만 있다.

- **설정:** `presets: [{ "slot": 1–10, "name": "게임 모드", "type": "program"|"url"|"script", "path": "…", "args": ["…"] }]`.
  - `program`: exe를 인자 배열 그대로 실행(셸 없음). `url`: 기본 브라우저로 연다(http/https만). `script`: `.ps1`/`.bat`/`.cmd` 파일 경로를 고정 인터프리터로 실행.
  - 모두 **사용자 세션에서** 실행한다(SYSTEM 권한으로 실행하지 않는다). 사용자가 없으면 `no_user_session`.
- **API:** status `presets: [{slot, name}]`, `features`에 "presets". command `preset`(value = 슬롯 번호). 실행 결과(시작 성공/실패)는 `last_command`에 남긴다.
- **텔레그램:** `/presets`(목록), `/run 이름|번호`.
- **데스크톱 앱:** 명령 탭에 프리셋 목록과 [실행], 설정에 편집기(이름·종류·경로·인자·[찾아보기]·[테스트]).
- **드라이버 제약:** SmartThings 목록 항목은 프레젠테이션에 고정된다. 그래서 목록은 "프리셋 1 (Preset 1)"…"프리셋 10" 슬롯이고,
  비어 있는 슬롯은 `supportedValues`로 숨긴다(실측 대기). 슬롯 이름은 별도 줄 "1 게임 모드 · 2 방송 시작 …"으로 보여 준다.
  무동작 쉬는 값 `none`(목록 닫기 대비, 플랫폼 노트). 커스텀 capability `pcPreset`: `run(slot)`, `lastPreset`, `names`, `supportedSlots`.
- **보안:** 원격에서 경로·인자를 받지 않는다. 슬롯 번호 외 입력은 거부. 설정 변경 알림(보안 카테고리)에 프리셋 변경 포함.

## 11. 실행 중 앱 감지 (옵트인)

- **설정:** `activity: { "enabled": false, "watch": [{ "process": "steam.exe", "label": "Steam", "kind": "game"|"work"|"media"|"stream"|"other" }] }`, 최대 20개.
- 서비스가 10초마다 프로세스 목록(세션 무관)을 훑어 감시 목록과 **대소문자 무시 파일 이름 일치**만 본다. 목록에 없는 프로세스 이름은 어디에도 보내지 않는다.
- **API:** status `activity: { enabled, kind: "game"|…|"none", labels: ["Steam"] }`. 여러 개가 켜져 있으면 kind 우선순위 game > stream > media > work > other.
  바뀌면 푸시 `activity.changed`.
- **드라이버:** 커스텀 `pcActivity` — `activity` enum(루틴 조건용), `summary` "게임 중 · Steam" / "없음" / 옵트인 꺼짐이면 "꺼짐".
- **데스크톱 앱:** 설정의 감시 목록 편집기(실행 중 프로세스에서 고르기 지원).
- **구현(#110):** `process`는 경로 없는 `.exe` 파일 이름, `label` 30자 이하(비우면 파일 이름에서 `.exe`를 뗀 값), 목록 안 중복 불가.
  저장 시 400으로 거절하고, 불러올 때는 잘못된 항목만 로그와 함께 건너뛴다. `labels`는 kind 우선순위 → 목록 순서로 중복 없이.
  `features`의 `"activity"`는 켜져 있을 때만 붙는다. 꺼져 있으면 프로세스 목록을 읽지 않는다. 목록을 고치면 이전 스캔 결과는
  쓰지 않고 다음 스캔(저장 직후 바로)까지 `none`. `activity.changed`는 켜기/끄기 전환에도 보낸다(데이터는 kind·labels만).
  편집기는 네트워크 탭 SmartThings 섹션(세션 정보 옆)에 있고, 고르기 목록은 `GET /api/processes`(세션 인증 + 루프백 전용)에서 온다.
  텔레그램 `/status`에 `활동: 게임 중 · Steam`(켜져 있고 실행 중일 때만). WebUI는 켜기/끄기만.

## 12. 잠들지 않기

- 서비스가 `SetThreadExecutionState(ES_CONTINUOUS|ES_SYSTEM_REQUIRED)`를 전용 고루틴에서 잡는다. **자동(유휴) 절전만** 막고
  사용자·원격의 종료·절전 명령은 막지 않는다. 화면 끄기는 막지 않는다(옵션 `keep_display`, 기본 끔).
- 기간: 1시간 기본, 설정 `awake.default_minutes`(0 = 끌 때까지). 서비스 재시작 시 남은 시간을 이어받지 않는다(안전 쪽).
- **API:** status `awake: { on, until }`, command `awake`(value 분, 0 = 무기한) / `awakeoff`. 푸시 `awake.changed`.
  - `until`은 RFC3339, 꺼져 있거나 무기한이면 `""`. `features`에 "awake".
  - `value`는 0–1440, 키가 없으면 `default_minutes`. `minutes`(예약)는 받지 않는다(400). 유예·예약·`last_command`·텔레그램 알림과 무관.
  - 켜져 있는 동안 다시 `awake`를 보내면 지금부터 새 기간(연장·단축 모두 이 방법).
  - 만료 타이머는 단조 시계라 수동 절전 동안 멈추므로, 30초마다와 status를 읽을 때 벽시계로도 만료를 확인한다.
  - 데스크톱 앱은 `GET/POST {minutes}/DELETE /api/awake`(WebUI 포트, 세션·CSRF)를 쓴다.
- **텔레그램:** `/awake [분]`, `/awake off`. 데스크톱 앱: 명령 탭 토글과 남은 시간.
- **드라이버:** 컴포넌트 `awake`에 표준 `switch`. 켜면 환경설정 `awakeMinutes`(기본 60) 동안. 표준 스위치라 루틴 동작·조건에 그대로 쓴다.

## 13. 노트북 배터리

- 서비스가 `GetSystemPowerStatus`로 `battery: { present, percent, charging, ac }`를 status에 싣는다(60초 주기로 충분).
  - `percent`는 0–100, 모르면 -1. `BatteryFlag` 128(시스템 배터리 없음)이면 `present=false`. 255(알 수 없음)는 잔량을 알면 있는 것으로 본다.
    `charging`은 플래그 비트 8, `ac`는 `ACLineStatus` 1. 배터리가 없으면 percent -1·charging false로 고정.
  - 바뀌면 푸시 `battery.changed`(첫 읽기는 변화로 치지 않음). `features`에 "battery"는 `present`일 때만.
  - 텔레그램 `/status`와 앱 상단 상태 줄은 배터리가 있을 때만 한 줄을 더한다. 앱은 `GET /api/battery`를 1분마다 읽는다.
- **드라이버:** 표준 `battery`·`powerSource`. 데스크톱에 빈 줄이 생기지 않도록 **배터리가 있을 때만** 컴포넌트 `battery`가 있는
  프로필 변형(`pc-<style>-battery.v2`)으로 옮긴다. 아이콘 10종 × 배터리 유무 = 20개 프로필은 손으로 관리하지 않고
  `tools/gen-profiles.js`가 `profiles/pc.yml` 하나에서 생성한다(동기 테스트가 생성 결과와 파일을 비교).
- 루틴 예: "배터리 20% 이하면 충전기 플러그 켜기".

## 14. 프로필 pc.v2 구성

- main: switch, refresh, pcPower, pcRemote, pcDefer, pcUser, pcInfo, pcVersion, audioVolume, audioMute, mediaPlayback, mediaTrackControl, pcPreset, pcActivity, (pcNotify)
- awake: switch
- battery(배터리 변형만): battery, powerSource
- 이름: `pc.v2`, `pc-<style>.v2`, `pc-battery.v2`, `pc-<style>-battery.v2`. `pc*.v1`은 `KNOWN`으로 자동 이전.

## 15. 재생 정보와 앱 단위 재생 제어 (#117 / #118)

Windows 10 1809+의 `Windows.Media.Control.GlobalSystemMediaTransportControlsSessionManager`(WinRT)는
재생 중인 앱의 세션을 모아 준다. 이것으로 곡 정보를 읽고, 미디어 키 대신 **세션에 직접** 재생·일시정지를 보낸다.

- **읽는 곳:** 사용자 세션의 트레이 앱. 3초마다 현재 세션을 확인하고, 바뀌면 즉시 하트비트(`sampled_at` 포함)로 보낸다.
  트레이 앱이 없으면 곡 정보는 비고, 제어는 `user-action media`가 세션 API → 실패 시 미디어 키 순으로 처리한다.
- **보내는 값:** `media: { status: playing|paused|stopped|none, title, artist, album, app, updated_at }`.
  `status`는 제어 정확도를 위해 `media.enabled`면 보낸다. `title`·`artist`·`album`·`app`은 옵트인 `media.now_playing`(기본 끔)일 때만.
  파일 경로·URL·썸네일은 보내지 않는다. 크롬 등 브라우저는 탭 제목(유튜브 영상 제목)이 제목으로 오므로 옵트인 설명에 적는다.
- **제어:** `play`/`pause`는 이제 구분된다(TryPlayAsync/TryPauseAsync). `playpause`는 토글, `stop`/`next`/`prev`는 해당 메서드.
- **API:** status `media` 블록, features += "nowplaying"(옵트인일 때), 푸시 `media.changed`. 텔레그램 `/np`, `/status` 한 줄.
- **드라이버:** 표준 `audioTrackData`(title/artist/album)와 `mediaPlayback.playbackStatus`. 미디어 묶음을 main에 둘지 컴포넌트 `media`로
  분리할지는 Dev 채널에서 둘 다 그려 보고 정한다(실측 대기).

### UI 구성

- **데스크톱 앱 명령 탭** — 카드 순서: 전원 → **미디어** → 잠들지 않기 → 프리셋.
  - 미디어 카드: 첫 줄 재생 정보 `▶ 제목 — 아티스트 · Spotify`(옵트인 꺼짐이면 `재생 중` / `일시정지`만, 세션 없으면 `재생 중인 미디어 없음`),
    둘째 줄 ⏮ ⏯ ⏭, 셋째 줄 🔈 볼륨 슬라이더(0–100, 놓을 때 전송) + 음소거 토글 + 현재 장치 이름.
  - 긴 제목은 한 줄로 자르고 전체는 툴팁.
- **데스크톱 앱 설정의 미디어·알림 섹션** — 미디어 제어 허용 / 재생 정보 공유(옵트인, 설명 한 줄) / PC 알림 허용 / 소리내어 읽기 + 음성 / [테스트 알림].
- **SmartThings 상세 화면** — 상태 카드와 조작 카드 뒤에 미디어 묶음: 곡 정보 → 재생/일시정지·이전/다음 → 볼륨 슬라이더 → 음소거.
  그 뒤 프리셋(목록 + 이름 줄), 활동, PC 알림 입력, 잠들지 않기·배터리 컴포넌트.
