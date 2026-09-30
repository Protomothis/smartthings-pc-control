# 설계: 오디오·미디어 제어와 PC 알림 (v1.2.0 / Edge 1.1.0)

전원만 다루던 PC Control에 **볼륨·음소거**, **미디어 제어**, **PC에 알림 띄우기**를 더한다.
세 기능 모두 SmartThings 앱·루틴·텔레그램에서 쓸 수 있어야 한다.

## 1. 목표와 범위

| 기능 | SmartThings | 텔레그램 | 비고 |
|---|---|---|---|
| 볼륨 | 슬라이더, 올리기/내리기, 현재 값 표시 | `/vol 30`, `/vol +10` | 기본 재생 장치 기준 |
| 음소거 | 켜기/끄기, 현재 상태 | `/mute`, `/unmute` | |
| 미디어 | 재생/일시정지/정지, 다음/이전 곡 | `/play` `/pause` `/next` `/prev` | 미디어 키 전송. 재생 상태는 1차에서 보고하지 않음 |
| PC 알림 | 루틴 동작 "PC에 알림"(문구) | `/say 문구` | 토스트 기본, 소리내어 읽기는 설정으로 켬 |

**범위 밖(2차 후보, 별도 마일스톤):** 프리셋 실행, 실행 중 앱 감지, 잠들지 않기, 노트북 배터리.
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
2. 서비스 기능: 오디오 (#104), 미디어 (#105), PC 알림 (#106) + 텔레그램
3. 데스크톱 앱: 미디어·알림 섹션 (#106에 포함)
4. 드라이버: 표준 capability와 pc.v2 (#107), PC 알림 동작 (#108), 세션 없음·옛 서비스 표시 (#107에 포함)
5. Dev 채널 실측 → 수정 → v1.2.0 릴리스 → 공개 채널 edge-v1.1.0

드라이버는 서비스 v1.2.0이 먼저 나가야 의미가 있으므로 **앱 릴리스 → 드라이버 공개** 순서로 낸다.
