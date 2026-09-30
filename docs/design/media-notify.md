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
  (#104 구현: go-ole 없이 `useraction/coreaudio.go`가 필요한 vtable 슬롯 몇 개를 `syscall.SyscallN`으로 직접 부른다. 새 의존성 없음.)
  미디어 키는 `SendInput`(VK_MEDIA_*), 음성은 SAPI `SpVoice`(go-ole, `internal/sapi`).
  토스트는 go-toast를 **쓰지 않는다**(#106): go-toast는 제목·문구를 PowerShell 큰따옴표 here-string에 그대로
  넣어 `$(…)`가 실행된다. 대신 토스트 XML을 Go에서 이스케이프해 환경 변수로 고정 스크립트(`-EncodedCommand`)에 넘긴다.
  AppID는 트레이 앱과 같은 "SmartThings PC Control". 트레이 앱의 고정 문구 토스트는 지금처럼 go-toast.

### user-action 확정 문법 (#103)

위 목록에서 `notify --voice`와 `preset`이 늘었고 오류 모양을 정했다. 구현은 `useraction/`.

- `audio get` · `audio set <0-100>` · `audio step <-100..100>`(`+5`·`-5`·`5`) · `audio mute <on|off|toggle>`
- `media <playpause|play|pause|stop|next|prev>`
- `notify --title <t> --text <t> [--speak] [--voice <name>]` — 값은 다음 인자를 그대로 받는다(`--`로 시작해도 문구).
  `--text` 1–200자, 제목 100자, 제어 문자는 거절(서비스가 먼저 지운다).
- `preset --type <program|url|script> --path <p> [--arg <a>]...` — `url`은 http/https만·`--arg` 없음,
  `script`는 `.ps1`/`.bat`/`.cmd`만, `--arg` 최대 32개.
- `speak --text <t> [--voice <name>]` (#106, 내부용) — `notify --speak`가 토스트를 띄운 뒤 분리해서 띄우는 읽기 프로세스.
  200자를 읽는 데 3초 제한보다 훨씬 오래 걸리므로 notify는 음성만 정하고(`voice_used`, `voice_found`) 곧바로 답한다.
  읽기 프로세스는 이름 있는 뮤텍스로 줄을 서서 차례로 읽는다. 서비스는 이 동작을 만들지 않는다.
- 사용자 세션의 자식은 서비스(SYSTEM)의 환경 변수를 물려받는다. 프리셋이 띄우는 프로그램에는
  `CreateEnvironmentBlock`으로 만든 사용자 자신의 환경을 준다(#109).
- 출력은 stdout 한 줄: `{"ok":true,...}`(종료 0) 또는 `{"ok":false,"error":"<code>","message":"..."}`(종료 1).
  코드는 `bad_args` · `unsupported`(처리기가 없거나 이 PC에서 못 함) · `failed`.
- 기능 이슈는 `useraction.Register(action, handler)`로 처리기를 붙인다. 붙기 전에는 `unsupported`.
- 서비스는 `runUserAction`으로 부른다: 같은 파서로 먼저 검사(잘못된 인자는 프로세스를 띄우지 않음),
  3초 제한(넘으면 자식 종료), 출력의 **마지막 비지 않은 줄**을 JSON으로 읽는다. 결과에 `audio`가 있으면 저장값을 갱신한다.
- 하트비트 본문의 `idle_seconds`와 `audio`는 각각 선택이다. `audio`가 범위를 벗어나면 본문 전체를 400으로 거절한다.
- (#104) 트레이는 `audio`에 `sampled_at`(RFC3339, 읽은 시각)을 싣고, 저장값의 "더 새 값만" 비교는 받은 시각이 아니라
  이 시각으로 한다(명령 결과는 완료 시각). 없으면 받은 시각, 미래 값은 받은 시각으로 자르고, 형식이 틀리면 400.
  트레이는 `expose_session`과 무관하게 `media.enabled`가 켜져 있으면 `audio`를 보낸다(같은 Core Audio 코드를 프로세스 안에서 부름).
- (#104) 사용자 세션 찾기는 PowerShell `Get-Process explorer` 대신 `WTSGetActiveConsoleSessionId` →
  `WTSQueryUserToken`, 콘솔에 사용자가 없으면 `WTSEnumerateSessions`의 활성 세션(RDP)을 본다. 토큰이 없는 세션만
  "사용자 없음"이고, 권한 부족 같은 다른 실패는 그대로 오류로 올린다.

## 3. 서비스 API 추가 (`/st/v1`, protocol 1 유지)

- **status**에 추가:
  - `features: ["audio","media","notify"]` — 드라이버가 기능 유무를 판단한다(옛 서비스는 키 없음).
  - `audio: { available, volume, muted, device, updated_at }` — 마지막 하트비트나 명령 결과.
- **command**(`POST /st/v1/command`)에 명령 추가. 기존 `command`/`mode`/`minutes` 옆에 `value`를 둔다.
  - `volume`(value 0–100), `volumeup`/`volumedown`(value 기본 5), `mute`/`unmute`
  - `play`/`pause`/`playpause`/`stop`/`next`/`prev`
  - 유예·예약과 무관하게 **즉시 실행**. 텔레그램 알림은 기본 끔(볼륨 조절마다 알림이 오면 소음).
  - (#104·#105 구현) `volumeup`/`volumedown`의 value는 1–100, `volume`은 value 필수. `minutes`를 주면 400, `mode`는 무시.
    `last_command`에 남기지 않는다. 볼륨 변경은 음소거를 건드리지 않는다. 오류: `403 media_disabled`, `409 no_user_session`,
    `400`(범위), `501 unsupported`(재생 장치 없음), `502 failed`, `504 timeout`. 볼륨·음소거 응답에는 바뀐 `audio` 블록이 실린다.
  - `audio.available`은 사용자 세션이 없거나, 서비스 시작 뒤 값이 아직 없거나, `media.enabled`가 꺼져 있으면 false이고 나머지 키는 없다.
    `features`의 "audio"·"media"는 `media.enabled`일 때만.
  - (#105) `user-action media`는 백엔드 목록(`mediaBackends`)을 차례로 시도해 처음 처리한 쪽이 이긴다. 지금은 SendInput 미디어 키
    하나뿐이고, #117의 WinRT 세션 관리자는 그 앞에 붙어 세션이 없거나 실패하면 키로 넘긴다. 답은 `{"ok":true,"media":"next","via":"keys"}`.
- **notify**(`POST /st/v1/notify`): `{ "title"?: string, "text": string, "speak"?: bool }`.
  - `text` 1–200자, 제목 기본값은 "SmartThings". 제어 문자 제거.
  - 출처 IP별 분당 10회. 설정에서 끄면 `403 notify_disabled`.
  - 구현(#106): 넘으면 `429 rate_limited` + `Retry-After`(초), 문구 규칙 위반은 `400 bad_text`, 사용자 없음 `409 no_user_session`,
    시간 초과 `504 timeout`. 줄바꿈·탭은 공백으로, 제어 문자와 방향 제어 문자(U+202A–202E, U+2066–2069 등)는 지운다.
    응답 `{ok:true, toast:"shown"|"pending", spoken, voice_used?, voice_found?}`. `speak`는 `notify_pc.speak`가 켜져 있을 때만.
    오류 본문은 `{error: <code>, message}`. `features`의 "notify"는 설정과 무관하게 붙는다(media와 다름): 드라이버가 보내고 403을 "PC 알림 꺼짐"으로 보여 준다.
- 푸시(`/pc/evt`)에 `audio.changed` 이벤트를 더한다.
  - (#104) 명령 결과든 하트비트든 저장된 값(볼륨·음소거·장치)이 바뀌면 보낸다. 서비스 시작 뒤 첫 값은 변화로 치지 않는다.
    `data`는 `{volume, muted, device}`(문자열).

## 4. 설정 (config.json)

```json
"media": { "enabled": true },
"notify_pc": { "enabled": true, "speak": false, "voice": "" }
```

- `media.enabled`: 볼륨·미디어 명령 허용(기본 켬).
- `notify_pc.enabled`: 외부에서 PC 화면에 알림을 띄우는 것 허용(기본 켬). `speak`: 소리내어 읽기(기본 끔).
  `voice`: SAPI 음성 이름, 비우면 시스템 기본(한국어 음성은 Windows 언어 팩에 따라 없을 수 있음).
- 데스크톱 앱: **설정 탭의 미디어·알림** 섹션(#106) — 맨 위에 `media.enabled`·`media.now_playing`(#104·#117이 서비스 설정에 두었던 것을 옮김),
  그 아래 PC 알림 허용 · 소리내어 읽기 · 음성 · [테스트 알림]. 설정 탭의 저장 막대를 같이 쓴다. 음성 목록은 앱이 사용자 세션에서
  SAPI로 직접 읽는다(서비스는 세션 0이라 사용자의 음성을 볼 수 없다).

## 5. Edge 드라이버 (1.1.0)

| 기능 | capability | 종류 | 화면 |
|---|---|---|---|
| 볼륨 | `audioVolume` | 표준 | 상세 화면 슬라이더 |
| 음소거 | `audioMute` | 표준 | 토글 |
| 미디어 | `mediaPlayback` + `mediaTrackControl` | 표준 | 재생/일시정지, 이전/다음 버튼 |
| PC 알림 | 커스텀 `pcMessage`(`numbersystem53811.pcmessage`): `send(text)` · `speak(text)`, 속성 없음. 처음 계획한 표준 `notification`·`speechSynthesis`는 앱이 "텍스트 표시"·"음성 합성"으로 보여 줘서 바꿨다(아래) | 커스텀 | 루틴 동작과 상세 화면의 문구 입력 줄 둘: "PC에 메시지 보내기", "PC에서 소리내어 읽기" |

- 표준 capability는 정의 캐시 문제가 없고 앱 기본 UI를 그대로 쓴다. SmartThings에 연결된 음성 비서가
  볼륨을 인식하는지는 Dev 채널에서 확인한다.
- `mediaPlayback.playbackStatus`는 1차에서 보고하지 않는다. 버튼만 쓰고 상태는 `stopped`로 고정하지 않도록
  `supportedPlaybackCommands`만 방출하고 재생 상태 줄이 비는지 실측한다(비면 "알 수 없음" 대체값을 검토).
- **프로필:** capability가 늘어나므로 `pc.v2`로 올린다. 아이콘 변형 10개(`pc-<style>.v2`)를 함께 올리고
  `pc*.v1`은 `KNOWN`에 넣어 자동 이전한다(§6.6 규칙). 이전 직후 `repaint_soon`.
- **옛 서비스(features 없음):** 볼륨 줄은 비활성 안내("서비스 v1.2.0 필요")를 요약에 쓰고 명령은 보내지 않는다.
- **사용자 세션 없음:** `audio.available=false`면 명령을 보내지 않고 요약에 "사용자 없음".
- **PC 알림(#108):** `pcMessage.send(text)` → `POST /st/v1/notify {text}`(제목은 보내지 않아 서비스 기본 "SmartThings"),
  `pcMessage.speak(text)` → `{text, speak: true}`. 제어 문자를 지우고 200자(코드 포인트)에서 "…"로 자른다(서비스 한도 —
  정의의 `maxLength`와 입력 줄의 `range [1, 200]`도 같은 200이다). `features`에 "notify"가 있어야 보낸다. 결과는
  `pcInfo.message`에만 쓴다(루틴이 자주 보낼 수 있어 요약 줄을 덮지 않는다): 보냈으면 "PC에 메시지를 보냈습니다"(읽기는
  "PC에서 읽었습니다"), 옛 서비스 "서비스 v1.2.0 필요", `403 notify_disabled` "PC 알림 꺼짐", `409 no_user_session` "사용자 없음",
  `429` "잠시 후 다시". 속성이 없어 칠할 줄이 없다.
- **왜 표준이 아니라 `pcMessage`인가:** 처음에는 표준 `notification`(`deviceNotification`, live)과 `speechSynthesis`(`speak`,
  proposed)로 충분하다고 보았다 — 둘 다 detailView와 `automation.actions`에 `textField`가 있고 속성도 없다. 그런데 휴대폰은
  이 줄을 삼성의 번역으로 "텍스트 표시", "음성 합성"이라 부르고, 장치 쪽(프로필·임베디드 장치 구성)에서 표준 capability의
  라벨을 바꿀 방법이 없다(플랫폼 노트 "표준 capability"). "PC에 메시지 보내기"라고 읽히려면 라벨이 우리 번역 파일에 있어야
  하므로 명령 둘짜리 커스텀 capability를 만들었다. 모양은 표준 `notification`을 그대로 따른다(`textField` + `argumentType:
  "string"`, 인자 하나). 새로 만드는 capability라 정의 캐시 문제는 없다. 화면이 바뀌므로 프로필은 `pc.v3`이다(§14).
- `pc.v2`(Dev 채널)의 장치를 위해 드라이버는 표준 두 명령의 핸들러도 계속 등록한다. 첫 `init`에서 v3로 옮겨지지만, 허브가
  이전을 거절하면 그 구동 동안은 v2에 남기 때문이다. v2의 루틴 동작 "텍스트 표시"는 v3로 옮긴 뒤 사라지므로 다시 고른다.

## 6. 텔레그램

`/vol [0-100|+n|-n]`, `/mute`, `/unmute`, `/play`, `/pause`, `/next`, `/prev`, `/say 문구`.
`/vol`만 치면 현재 볼륨과 음소거 상태를 답한다. `/say`는 `notify_pc.enabled`를 따른다.

- (#104 구현) `/mute`·`/unmute`는 원래 알림 일시 중지(v1.0)였다. 이제 인자 없는 `/mute`와 `/unmute`는 PC 음소거이고,
  알림 일시 중지는 `/quiet 30m|2h|off`로 옮겼다. `/mute 30m`처럼 시간을 붙이면 예전대로 알림을 멈춘다. 알림이 멈춘 동안
  `/unmute`는 음소거만 풀고 "`/quiet off`로 재개" 안내를 덧붙인다. `/stop`도 있다. 답은 `볼륨 30% · 음소거 꺼짐 · 스피커`,
  미디어 키는 `⏯ 재생/일시정지 키를 보냈습니다`.

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
  2. ~~표준 `notification`/`speechSynthesis`가 루틴 동작에 나오는가, 문구 입력이 되는가~~ — 계정에서 확인했다(#108): 둘 다 detailView와 automation.actions에 textField가 있다. 다만 라벨이 "텍스트 표시"·"음성 합성"이라 커스텀 `pcMessage`로 바꿨다(§5). 남은 것은 `pcMessage`의 두 입력 줄이 그렇게 그려지는지(§16)
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
  - 구현(#109): `program`은 절대 경로의 `.exe`/`.com`만(`.bat`을 CreateProcess에 주면 cmd.exe가 제 규칙으로 읽으므로
    배치 파일은 `script`로). `.ps1`은 `%SystemRoot%\…\powershell.exe -NoProfile -ExecutionPolicy Bypass -File`,
    `.bat`/`.cmd`는 `%SystemRoot%\System32\cmd.exe /d /v:off /s /c ""<path>" "<arg>"…"` — 모든 부분을 따옴표로 감싸고
    따옴표 안에서도 해석되는 `"`와 `%`는 경로·인자에서 거절한다. 나머지는 `syscall.EscapeArg`. 작업 폴더는 파일의 폴더.
  - 저장할 때 규칙에 어긋나면 400, 손으로 고친 `config.json`은 그 항목만 무시하고 로그를 남긴다.
- **API:** status `presets: [{slot, name}]`, `features`에 "presets"(프리셋이 없어도 — 드라이버가 옛 서비스와 빈 슬롯을 가른다). command `preset`(value = 슬롯 번호, 없으면 400,
  없는 슬롯은 `404 no_such_preset`). `last_command`에 `preset: {slot, name}`과 `result`("started" 또는 오류 코드).
  응답은 `{accepted, executed, schedule, preset: {slot, name, started}}`. 실행은 `remote.received`로도 알린다.
- **텔레그램:** `/presets`(목록), `/run 이름|번호`.
- **데스크톱 앱:** 명령 탭에 프리셋 목록과 [실행], 새 **프리셋** 탭(네트워크와 로그 사이)에 편집기(슬롯·이름·종류·경로·인자·[찾아보기]·[테스트]).
  설정 탭의 한 섹션이 아니라 탭인 것은 행 최대 10개 × 3줄이 서비스 설정을 밀어내기 때문이다.
  로컬 API `POST /api/presets/run {slot}`(저장된 것), `POST /api/presets/test {preset}`(저장 전 행).
- **드라이버 제약:** SmartThings 목록 항목은 프레젠테이션에 고정된다. 그래서 목록은 "프리셋 1 (Preset 1)"…"프리셋 10" 슬롯이고,
  비어 있는 슬롯은 `supportedValues`로 숨긴다(실측 대기). 슬롯 이름은 별도 줄 "1 게임 모드 · 2 방송 시작 …"으로 보여 준다.
  무동작 쉬는 값 `none`(목록 닫기 대비, 플랫폼 노트). 커스텀 capability `pcPreset`: `run(slot)`, `lastPreset`, `names`, `supportedSlots`.
- **보안:** 원격에서 경로·인자를 받지 않는다. 슬롯 번호 외 입력은 거부. 설정 변경 알림(보안 카테고리)에 프리셋 변경 포함
  — 키는 `presets[1,3]`처럼 바뀐 슬롯 번호만, 경로·인자는 싣지 않는다.

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
  `tools/gen-profiles.js`가 템플릿 `tools/profile-template.yml` 하나에서 생성한다(동기 테스트가 생성 결과와 파일을 비교). 템플릿이 `profiles/` 밖에 있는 것은 패키저가 그 폴더의 YAML을 전부 프로필로 올리기 때문이고, `profiles/pc.yml`은 v1 장치가 쓰는 고정 파일로 남는다(edge-driver.md §6.6).
- 루틴 예: "배터리 20% 이하면 충전기 플러그 켜기".

## 14. 프로필 pc.v3 구성

- main: switch, refresh, pcPower, pcRemote, pcDefer, pcUser, pcInfo, pcVersion, audioTrackData, mediaPlayback, mediaTrackControl, audioVolume, audioMute, pcPreset, pcActivity, pcMessage — 순서는 §15 "UI 구성"(미디어 묶음이 edge-v1.0 카드 뒤)
- `pc.v2`(Dev 채널)는 마지막 두 자리가 표준 `notification`, `speechSynthesis`였고, 그 밖에는 같다. `pcMessage`로 바꾸면서 v3가 됐고 v2 스무 개는 패키지에 고정으로 남는다(§5)
- media(대안 배치만, #118): audioTrackData, mediaPlayback, mediaTrackControl, audioVolume, audioMute — `gen-profiles.js --media-component`. 기본은 위의 main 배치
- awake: switch
- battery(배터리 변형만): battery, powerSource
- 이름: `pc.v3`, `pc-<style>.v3`, `pc-battery.v3`, `pc-<style>-battery.v3`. `pc*.v1`과 `pc*.v2`는 `KNOWN`으로 자동 이전(v2는 배터리 쪽도 이름 그대로).

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
- (#117 구현) WinRT는 새 의존성 없이 vtable을 직접 부른다(`useraction/winrt.go`). 비동기 결과는 완료 델리게이트 대신
  `IAsyncInfo.Status`를 5ms 간격으로 확인하고, `RequestAsync` 1.5초 · 나머지 1초에서 끊는다. `pause`는 재생 중이 아니면
  아무것도 보내지 않는다(앱이 거절해 키로 넘어가면 토글 키가 오히려 재생을 시작하므로). 앱이 거절(`false`)하거나 호출이 실패하면 키로 넘어간다.
  세션 답은 `{"ok":true,"media":"pause","via":"session","status":"paused","app":"Spotify"}`, 조회는 `user-action media info` →
  `{"ok":true,"media":{"status","title","artist","album","app"}}`. `app`은 AUMID를 표시 이름으로 바꾼 것(Spotify · Chrome · Edge ·
  Firefox · VLC · foobar2000 …, 기본 앱은 표시 언어에 따라 `미디어 플레이어`/`Media Player`, 모르면 AUMID 끝부분).
- (#117 구현) status `media`는 늘 있고 `status`만은 `media.enabled`면 온다. 세션이 없거나, 로그인한 사용자가 없거나, 90초 넘게
  새 표본이 없으면(트레이 앱이 없음) `none`. 옵트인이 꺼져 있으면 저장할 때도 보여 줄 때도 곡 정보를 뺀다. 미디어 명령 뒤에는 답의
  상태를 바로 저장하고(같은 앱이면 곡 정보 유지) 1.2초 뒤 `media info`로 다시 읽는다. 트레이는 3초마다 오디오·미디어를 읽어 바뀐 블록만
  하트비트로 보낸다(30초 하트비트는 전부). 앱의 미디어 카드는 새 로컬 API `GET/POST /api/media`를 쓴다.
- **드라이버:** 표준 `audioTrackData`(title/artist/album)와 `mediaPlayback.playbackStatus`. 미디어 묶음을 main에 둘지 컴포넌트 `media`로
  분리할지는 Dev 채널에서 둘 다 그려 보고 정한다(실측 대기).
  - (#118 구현) `playbackStatus` ← `media.status`(`none` → `stopped`), 블록이 없으면 내보내지 않는다. `audioTrackData`는 `features`에
    "nowplaying"이 있고 제목이 있을 때만 `{title, artist?, album?}`(빈 필드는 빼고 `""`는 보내지 않는다, 128자에서 자름). 블록은 있는데
    제목이 없으면 `{title: "재생 중인 미디어 없음"}`, 옵트인이 꺼져 있으면 `{title: "재생 정보 꺼짐"}` — 멈춘 곡의 제목이 화면에 남지 않게.
    `app`은 보내지 않는다. 푸시 `media.changed`는 전체 status를 싣고 와 바로 반영된다.
  - **배치 기본값은 main**이다(템플릿 순서가 위 "UI 구성"). `bun tools/gen-profiles.js --media-component`가 미디어 묶음을 컴포넌트
    `media`(label "미디어")로 뺀 대안 배치를 쓴다 — Dev 채널 비교용이고 커밋하지 않는다(edge-driver.md §6.6). 드라이버는 두 배치를 다 받는다.

### UI 구성

- **데스크톱 앱 명령 탭** — 카드 순서: 전원 → **미디어** → 잠들지 않기 → 프리셋.
  - 미디어 카드: 첫 줄 재생 정보 `▶ 제목 — 아티스트 · Spotify`(옵트인 꺼짐이면 `재생 중` / `일시정지`만, 세션 없으면 `재생 중인 미디어 없음`),
    둘째 줄 ⏮ ⏯ ⏭, 셋째 줄 🔈 볼륨 슬라이더(0–100, 놓을 때 전송) + 음소거 토글 + 현재 장치 이름.
  - 긴 제목은 한 줄로 자르고 전체는 툴팁.
    (#117 구현) Fyne 2.8에는 툴팁이 없어, 잘렸을 때만 전체 문구를 바로 아래 작은 글씨로 보여 준다. 일반 → 전원 → 미디어 →
    잠들지 않기 순이고, "즉시 실행" 안내는 전원 카드 안으로 옮겼다.
- **데스크톱 앱 설정의 미디어·알림 섹션** — 미디어 제어 허용 / 재생 정보 공유(옵트인, 설명 한 줄) / PC 알림 허용 / 소리내어 읽기 + 음성 / [테스트 알림].
- **SmartThings 상세 화면** — 상태 카드와 조작 카드 뒤에 미디어 묶음: 곡 정보 → 재생/일시정지·이전/다음 → 볼륨 슬라이더 → 음소거.
  그 뒤 프리셋(목록 + 이름 줄), 활동, PC 메시지 입력 두 줄(`pcMessage`), 잠들지 않기·배터리 컴포넌트.

## 16. 드라이버 1.1.0 Dev 채널 실측 대기

드라이버가 정해 둔 가정 중 기기에서만 확인할 수 있는 것. 확인되면 결과를 `edge-platform-notes.md`로 옮기고 여기서 지운다.

1. **미디어 묶음 모양(#107)** — `mediaPlayback`·`mediaTrackControl`·`audioVolume`·`audioMute`가 상세 화면에 어떻게 그려지는지, 우리 상태·조작 카드와 섞이는지 따로 그려지는지, 대시보드 타일이 바뀌는지.
2. **재생 상태를 보고하지 않을 때(#107, #118 이후에는 `media` 블록이 없는 서비스만)** — `playbackStatus`를 한 번도 내보내지 않으면 재생 줄이 "-"인지, 앱이 "상태를 모두 보고하지 않았다"고 하는지, 재생/일시정지를 누르면 회전 표시 뒤 오류로 끝나는지. 그렇다면 `features.PLAYBACK_RESTING = "stopped"` 한 줄로 쉬는 값을 켠다(명령마다 그 값을 강제로 내보낸다).
3. **읽은 적 없는 볼륨(#107)** — 옛 서비스나 로그인 전 PC에서는 `volume`·`mute`를 내보내지 않는다. 슬라이더가 비어 보이는 모양과 "모두 보고하지 않음" 안내가 뜨는지.
4. **음성 비서(선택, #107)** — SmartThings에 연결된 음성 비서가 이 장치의 볼륨을 인식하는지.
5. **빈 프리셋 슬롯 숨기기(#113)** — `pcPreset` 목록의 `supportedValues: "supportedSlots.value"`가 등록된 슬롯만 남기는지, 하나도 없을 때(`["none"]`) 목록이 비는지 전체가 보이는지, 슬롯이 바뀌면 다시 그려지는지. 먹지 않으면 빈 슬롯은 "프리셋 7 비어 있음"으로 막힌다(드라이버 가드).
6. **프리셋 실행 표시(#113)** — "프리셋 3 실행함"이 보였다가 다음 폴링에 "프리셋 선택…"으로 돌아오는지, 그 사이 목록을 닫아도 다시 실행되지 않는지. 이름 줄이 상태 카드의 어디(버전 줄 아래?)에 그려지는지.
7. **활동 줄과 루틴 조건(#114)** — `pcActivity.summary`가 상태 카드의 어디에 그려지는지, 루틴 조건 목록에 "활동"이 병기 문구(`게임 (Game)`)로 나오는지, `activity.changed` 푸시 뒤 루틴이 바로 도는지.
8. **잠들지 않기 컴포넌트(#115)** — 프로필 컴포넌트의 `label: 잠들지 않기`가 패키징을 통과하고 화면에 그 이름으로 보이는지(안 되면 컴포넌트 id `awake`만 보인다), 토글이 상세 화면 어디에 그려지는지, 루틴 동작·조건 목록에 "잠들지 않기" 스위치가 따로 나오는지, 대시보드 타일의 토글이 여전히 main(전원)인지.
9. **배터리 프로필 이동(#116)** — 노트북이 `pc-<style>-battery.v2`로 옮겨진 뒤 배터리 컴포넌트(`label: 배터리`)가 잔량·전원 공급원을 보여 주는지, 옮긴 직후의 빈 줄이 15초·90초 다시 칠하기로 채워지는지, 루틴 조건 "배터리 20% 이하"가 이 장치에서 고를 수 있는지. 데스크톱은 빈 카드 없이 `pc-<style>.v2`에 머무는지.
10. **PC 메시지(#108, `pcMessage`)** — 상세 화면에 "PC에 메시지 보내기"·"PC에서 소리내어 읽기" 입력 줄 둘이 우리 라벨로 그려지는지, 루틴 동작 목록에 같은 두 동작이 나오는지, `range [1, 200]`이 입력 길이를 막는지, 문구를 보낸 뒤 앱이 회전 표시 없이 끝나는지(속성이 없어 답할 줄이 없다 — 결과는 메시지 줄의 "PC에 메시지를 보냈습니다"), v2 장치가 첫 `init`에서 같은 아이콘·배터리 쪽의 v3로 옮겨지는지.
11. **미디어 묶음 배치 비교(#118)** — 기본(main 안)과 `--media-component`(컴포넌트 `media`) 두 패키지로 그려 보고 깔끔한 쪽을 고른다. 두 배치의 프로필 이름이 같으므로(`pc*.v3`) 이미 v3에 올라탄 장치는 패키지를 바꿔도 화면이 굳어 있다(플랫폼 노트 "프로필과 화면 생성") — 배치마다 장치를 지우고 다시 추가해서 본다. 컴포넌트 쪽이면 템플릿의 `@media-*` 블록을 그 배치로 고정하고 프로필 버전을 올린다(pc.v4 — 장치 화면은 프로필 이름으로 굳으므로 같은 v3 이름으로 바꿔 올리면 반영되지 않는다).
12. **곡 정보·재생 상태(#118)** — `audioTrackData`의 제목·아티스트가 어떻게 그려지는지(자리표시 문구 "재생 중인 미디어 없음" 포함), 재생/일시정지 버튼이 `playbackStatus`에 따라 모양을 바꾸는지, `media.changed` 푸시 뒤 바로 바뀌는지.
