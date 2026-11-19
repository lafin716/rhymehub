# rhymehub 사용 매뉴얼: octobooks 예제

이 문서는 [lafin716/octobooks](https://github.com/lafin716/octobooks)를 예로 들어, rhymehub에 프로젝트를 등록하고
AI 에이전트(Claude Code)에게 작업을 맡겨 리뷰·승인·릴리즈까지 하는 전체 흐름을 설명합니다. 마지막에는 octobooks
웹 UI에 rhymehub 피드백 위젯(SDK)을 붙여서, 앱 안에서 바로 개발 요청을 보내는 방법도 다룹니다.

> 대상 버전: rhymehub v0.1.0. API 경로는 모두 `/api/v1` 기준입니다. 상세 계약은 [API_SPEC](API_SPEC.md)을 보세요.

---

## 0. 전체 흐름

```
 (1) octobooks 등록 ──► (2) 테스트/빌드 명령 설정 ──► (3) 채팅으로 작업 생성
                                                         │  QUEUED → 실행 중 (worktree 안에서 Claude 실행)
                                                         │  ↳ 질문/권한 요청 시 WAITING_INPUT / WAITING_APPROVAL
                                                         ▼
 (6) GitHub에 push ◄── (5) 승인 → main 병합 → 태그 ◄── (4) diff·테스트 결과 리뷰 (REVIEW_READY)

 (7) octobooks 앱 안의 피드백 위젯 ──► Inbox ──► 작업 생성 ──► (3)부터 반복
```

핵심 원칙:

- 에이전트는 **작업마다 별도의 git worktree와 브랜치**에서 일합니다. 사용자의 작업 디렉터리
  (`~/workspace/octobooks`)는 승인 전까지 건드리지 않습니다.
- 실행되는 OS 명령은 **프로젝트에 설정한 test/build/release 명령뿐**입니다. 요청 본문이나 앱에서 온 텍스트는 절대
  명령으로 실행되지 않습니다.
- 자동 승인(autoApprove)과 자동 릴리즈(autoRelease)는 서로 다른 설정이며, **둘 다 기본값이 꺼짐**입니다.
- 에이전트는 GitHub에 push하지 못합니다(`git push`는 차단 목록에 있습니다). push는 사람이 직접 합니다.

---

## 1. 준비

### 1.1 서버 실행

```bash
cd ~/workspace/rhyme-hub
./bin/rhymehub init        # 처음 한 번: ~/.rhymehub 생성, 설정 파일 작성, DB 마이그레이션
./bin/rhymehub doctor      # home, DB, git, claude/codex 감지 결과 확인
./bin/rhymehub serve       # 0.0.0.0:7780
```

상시 실행하려면 supervisor를 사용합니다(자가 업데이트, 헬스체크, 롤백 포함).

```bash
./bin/rhymehub-supervisor bootstrap
./bin/rhymehub-supervisor run
./bin/rhymehub-supervisor install-launchd --load   # 선택: 로그인 시 자동 시작(macOS)
```

### 1.2 접속 주소

| 경로 | 주소 | 비고 |
|---|---|---|
| 같은 Mac | http://localhost:7780 | |
| Tailscale | http://rich.tailf4a14e.ts.net:7780 · http://100.102.77.120:7780 | 휴대폰에서 접속할 때 권장 |
| LAN | http://192.168.219.108:7780 | 기본값으로는 `403 tls_required`가 납니다. 아래 참고 |

LAN에서 평문 HTTP 접속은 의도적으로 막혀 있습니다. 다음 중 하나를 고르세요.

- **권장:** Tailscale 주소를 사용합니다.
- `~/.rhymehub/config.json`에 `tlsCert`/`tlsKey`를 설정해서 HTTPS로 엽니다.
- (비권장) 신뢰할 수 있는 네트워크라면 `insecureAllowedCIDRs`에 `192.168.219.0/24`를 추가합니다. 비밀번호와 세션
  쿠키가 평문으로 전송됩니다.

### 1.3 에이전트 로그인

```bash
claude        # Claude Code에 공식 CLI 로그인을 한 번 해 둡니다
./bin/rhymehub doctor   # "claude: available" 확인
```

rhymehub는 Claude/Codex의 자격 증명 저장소를 읽지 않습니다. 각 CLI의 공식 로그인을 그대로 사용합니다.

### 1.4 첫 관리자 계정

브라우저에서 대시보드를 열면 처음 한 번 **Setup 화면**(`/setup`)이 나옵니다. 사용자 이름과 비밀번호를 입력하면
admin 계정이 만들어지고 로그인됩니다.

역할은 네 가지입니다.

| 역할 | 할 수 있는 일 |
|---|---|
| reporter | 요청 작성, 진행 상황 조회 |
| developer | 프로젝트 등록, 작업 생성, 후속 지시, 취소/재시도 |
| maintainer | 승인/거절, 릴리즈, **명령·정책 변경**(testCmd, buildCmd, releaseCmd, allowedTools, autoApprove, autoRelease) |
| admin | 사용자 관리, 시스템 업데이트 |

다른 사람에게 접근 권한을 줄 때는 Settings에서 사용자를 만들고 필요한 최소 역할만 부여하세요.

---

## 2. octobooks 등록

octobooks는 pnpm 모노레포입니다(`packages/ui`, `core`, `server`, `bridge`, `relay`와 `apps/desktop`, `mobile`).
등록 방법은 세 가지이며 하나만 고르면 됩니다.

### 방법 A: 대시보드에서 로컬 경로로 등록 (권장)

1. **Projects → New**(`/projects/new`)로 이동합니다.
2. 로컬 경로에 `/Users/lafin/workspace/octobooks`를 입력합니다. git 저장소여야 합니다.
3. 이름은 `octobooks`로 둡니다.
4. 테스트 명령은 비워 두면 자동 감지됩니다. `pnpm-lock.yaml`과 `scripts.test`가 있으므로 `pnpm test`가 들어갑니다.
   다만 **3장의 설정으로 바꿔야 합니다.**

`Discover` 기능(`POST /projects/discover {root}`)으로 `~/workspace` 아래의 git 저장소를 찾아서 골라도 됩니다(깊이 2까지).

### 방법 B: CLI로 등록

```bash
cd ~/workspace/octobooks
~/workspace/rhyme-hub/bin/rhymehub register --path . --name octobooks
```

원격 서버에 등록하려면 API 토큰(5장)을 사용합니다.

```bash
RHYMEHUB_TOKEN=rht_xxx rhymehub register --path . --name octobooks --server http://rich.tailf4a14e.ts.net:7780
```

### 방법 C: GitHub URL로 복제해서 등록

로컬 클론을 건드리고 싶지 않다면 git URL로 등록합니다. rhymehub가 `~/.rhymehub/repos/<id>`에 별도로 클론합니다.

```bash
curl -s -X POST http://localhost:7780/api/v1/projects \
  -H "Authorization: Bearer $RHYMEHUB_TOKEN" -H 'Content-Type: application/json' \
  -d '{"name":"octobooks","gitUrl":"https://github.com/lafin716/octobooks.git"}'
```

> 이 방법을 쓰면 승인된 변경은 rhymehub가 가진 클론의 main에 병합됩니다. GitHub에 반영하려면 그 클론에서 push해야
> 합니다(6장).

---

## 3. 프로젝트 설정 (maintainer)

프로젝트 화면의 설정에서 다음과 같이 맞춥니다. API로 하려면 `PATCH /api/v1/projects/{id}`를 호출합니다.

| 항목 | octobooks 권장값 | 이유 |
|---|---|---|
| testCmd | `pnpm install --frozen-lockfile && pnpm -r test` | 아래 참고 |
| buildCmd | `pnpm -r build` (선택) | 승인 전 빌드 확인 |
| defaultBranch | `main` | |
| defaultAgent | `claude` | `codex`, `fake`(테스트용)도 가능 |
| releaseStrategy | 처음에는 `none`, 익숙해지면 `tag` | 4·6장 참고 |
| autoApprove / autoRelease | 끔 | 리뷰 없이 병합되는 것을 막음 |
| autoTaskFromRequests | 끔(처음에는) | 켜면 앱 피드백이 바로 작업이 됨 |
| allowedTools | 비워 둠 | 필요할 때 좁은 규칙만 승인 |
| timeoutMinutes / maxConcurrent | 기본값 | |

**testCmd에 `pnpm install`이 꼭 필요한 이유:** 테스트는 작업용 worktree 안에서 `sh -c <testCmd>`로 실행됩니다.
worktree는 git에서 새로 체크아웃한 디렉터리라서 `node_modules`가 없습니다. 설치 없이 `pnpm -r test`만 실행하면
`vitest: command not found` 같은 오류로 실패합니다. pnpm은 전역 스토어를 하드링크하므로 두 번째부터는 빠릅니다.

API 예시:

```bash
curl -s -X PATCH http://localhost:7780/api/v1/projects/octobooks \
  -H "Authorization: Bearer $RHYMEHUB_TOKEN" -H 'Content-Type: application/json' \
  -d '{
    "testCmd": "pnpm install --frozen-lockfile && pnpm -r test",
    "buildCmd": "pnpm -r build",
    "defaultAgent": "claude",
    "releaseStrategy": "none"
  }'
```

> 프로젝트 id는 대시보드 URL(`/projects/<id>`)이나 `GET /api/v1/projects`에서 확인하세요.

먼저 로컬에서 `cd ~/workspace/octobooks && pnpm install --frozen-lockfile && pnpm -r test`가 통과하는지
확인하세요. 원래부터 실패하는 테스트가 있으면 모든 작업이 FAILED로 끝납니다.

### octobooks의 `.claude/` 설정은 적용되지 않습니다

rhymehub는 Claude를 `--setting-sources=` 옵션으로 실행합니다. 따라서 octobooks 저장소의 `.claude/settings.json`과
사용자의 `~/.claude` 설정(hooks, allow 규칙)은 **로드되지 않습니다**. 에이전트가 worktree 안의 설정 파일을 고쳐서
자기 권한을 넓히는 것을 막기 위해서입니다. `CLAUDE.md`는 저장소 파일이므로 에이전트가 프로젝트 안내문으로 계속
참고할 수 있습니다.

---

## 4. 작업 맡기기

### 4.1 채팅으로 작업 생성

프로젝트 화면의 채팅 입력창에 요청을 쓰고 에이전트(`claude`)를 고른 뒤 보냅니다. 좋은 요청의 예:

```
packages/ui 설정 화면에 표시되는 앱 버전(VITE_APP_VERSION) 옆에 빌드 날짜를 함께 보여줘.
- 빌드 날짜는 vite.config.ts의 define으로 주입해.
- 기존 설정 화면 테스트가 있으면 함께 고치고, 없으면 간단한 vitest 테스트를 추가해.
- 다른 패키지는 수정하지 마.
```

좋은 요청을 쓰는 요령:

- **범위를 좁힙니다.** 패키지나 파일 단위로 지정하세요(`packages/ui`, `packages/core` 등).
- **완료 조건을 테스트로 표현합니다.** "테스트 추가", "기존 테스트 통과"처럼 씁니다.
- **하지 말아야 할 것을 적습니다.** 예를 들어 "의존성 추가 금지", "apps/desktop 수정 금지"처럼 씁니다.

API로 만들 때:

```bash
curl -s -X POST http://localhost:7780/api/v1/tasks \
  -H "Authorization: Bearer $RHYMEHUB_TOKEN" -H 'Content-Type: application/json' \
  -d '{"projectId":"octobooks","agent":"claude","priority":0,
       "prompt":"packages/core의 ... 버그를 고치고 회귀 테스트를 추가해"}'
```

### 4.2 진행 과정

작업 화면(`/tasks/<id>`)에서 이벤트가 실시간(SSE)으로 표시됩니다.

1. **QUEUED:** 스케줄러가 동시 실행 한도에 맞춰 대기시킵니다.
2. **실행:** `octobooks` 저장소에서 작업 브랜치와 worktree를 만들고 Claude가 코드를 수정합니다.
3. **검증:** 에이전트 턴이 끝나면 rhymehub가 worktree에서 testCmd와 buildCmd를 실행합니다.
4. **REVIEW_READY:** 리뷰 대기 상태입니다. 실패하면 **FAILED**가 됩니다.

### 4.3 에이전트의 질문 (WAITING_INPUT)

에이전트가 확인 질문을 하면 작업이 멈춥니다. 작업 화면에서 답변을 입력하면 이어서 진행합니다
(`POST /tasks/{id}/answer`).

### 4.4 권한 요청 (WAITING_APPROVAL)

에이전트가 허용되지 않은 명령을 쓰려 하면 권한 요청이 올라옵니다. 예를 들어 `pnpm --filter @branch/ui test`를 직접
실행하려는 경우입니다.

- **허용**을 누르면 그 명령에서 파생된 **좁은 규칙만** 부여됩니다. 예: `Bash(pnpm --filter:*)`, `Bash(pnpm test:*)`.
- 맨 `Bash`, `*`, `curl`, `sudo`, `git push`, `WebFetch` 같은 넓거나 위험한 규칙은 허용해도 부여되지 않습니다.
- 확실하지 않으면 **거부**하세요. 거부해도 에이전트는 다른 방법으로 계속 진행하고, 최종 검증은 어차피 testCmd로
  rhymehub가 합니다.

자주 승인하는 규칙이 있다면 maintainer가 프로젝트 `allowedTools`에 미리 넣어 둘 수 있습니다. 예:
`["Bash(pnpm test:*)", "Bash(pnpm --filter:*)"]`. 넓은 규칙은 검증 단계에서 거부됩니다.

### 4.5 후속 지시, 취소, 재시도

- **후속 지시:** 결과가 아쉬우면 채팅으로 "테스트 이름을 한국어로 바꿔줘"처럼 이어서 지시합니다. 같은 세션으로
  재개됩니다(`POST /tasks/{id}/messages`).
- **취소:** `POST /tasks/{id}/cancel`.
- **재시도:** FAILED나 CANCELLED 작업을 다시 실행합니다(`POST /tasks/{id}/retry`).

---

## 5. 리뷰

REVIEW_READY가 되면 알림이 옵니다. 작업 화면에서 다음을 확인하세요.

1. **Diff:** 변경 파일과 내용입니다(`GET /tasks/{id}/diff`). 요청 범위 밖의 파일이 바뀌지 않았는지 봅니다.
2. **테스트/빌드 결과:** testCmd와 buildCmd의 출력과 종료 코드입니다.
3. **에이전트 요약:** 무엇을 왜 바꿨는지 설명합니다.

직접 돌려보고 싶다면 worktree 브랜치를 로컬에서 확인할 수 있습니다.

```bash
cd ~/workspace/octobooks
git branch --list 'rhymehub/*'                 # 작업 브랜치 목록 (rhymehub/<taskId>)
git diff main...rhymehub/<taskId> --stat        # 변경 요약
```

브랜치는 rhymehub worktree(`~/.rhymehub/worktrees/` 아래)에 체크아웃되어 있으므로 `git switch`로는 열 수 없습니다.
앱을 직접 실행해 보려면 해당 worktree 디렉터리에서 `pnpm install && pnpm dev`를 실행하세요. 파일은 수정하지 마세요.

리뷰 결과에 따라 다음 중 하나를 고릅니다.

- 마음에 들지 않으면 후속 지시를 보내거나 **거절**합니다(`POST /tasks/{id}/reject`).
- 좋으면 6장의 승인으로 넘어갑니다.

---

## 6. 승인과 릴리즈 (maintainer)

**승인**(`POST /tasks/{id}/approve`)하면 작업 브랜치가 `main`에 병합됩니다. 이어서 프로젝트의 `releaseStrategy`에
따라 진행됩니다.

| 전략 | 동작 | octobooks에서 |
|---|---|---|
| `none` | 병합만 합니다 | 처음에 권장 |
| `tag` | 병합한 뒤 `vX.Y.Z` 태그를 만듭니다. 기능 변경이면 minor, 그 외에는 patch를 올리고, 태그가 없으면 `v0.1.0`부터 시작합니다 | 버전 태그를 쓰는 경우 |
| `command` | 병합한 뒤 설정한 `releaseCmd`를 실행합니다 | 예: `pnpm -r build`, 데스크톱 패키징 스크립트 |
| `rhymehub-self` | rhymehub 자기 자신 전용입니다 | 사용하지 않음 |

승인할 때 "release"를 함께 선택하면(`{"release": true}`) 병합 후 바로 릴리즈합니다. 병합만 하고 나중에 프로젝트의
Releases에서 수동으로 릴리즈해도 됩니다(`POST /projects/{id}/releases`).

상태는 APPROVED → MERGING → RELEASING → COMPLETED 순서로 바뀝니다.

### GitHub에 반영

에이전트와 rhymehub는 push하지 않습니다. 병합된 결과를 확인한 뒤 직접 push하세요.

```bash
cd ~/workspace/octobooks          # 방법 C로 등록했다면 ~/.rhymehub/repos/<id>
git log --oneline -5
git push origin main --follow-tags
```

> 사용자의 작업 디렉터리가 `main`에 체크아웃되어 있고 수정 중인 파일이 있으면 병합이 실패할 수 있습니다.
> rhymehub를 쓰는 동안에는 octobooks 작업 디렉터리를 깨끗하게 유지하거나, 방법 C(별도 클론)를 사용하세요.

---

## 7. octobooks 앱에 피드백 위젯 붙이기 (SDK)

octobooks 웹 UI(`packages/ui`, Vite + React)에 rhymehub 피드백 버튼을 달면, 앱을 쓰다가 바로 "이 버튼이 안 눌려요"
같은 요청을 보낼 수 있습니다. 요청은 rhymehub의 **Inbox**에 쌓이고, 거기서 작업으로 바꿀 수 있습니다.

> 이 장의 변경은 octobooks 저장소를 수정합니다. 직접 하거나, 4장의 방법으로 rhymehub 작업으로 맡겨도 됩니다.

### 7.1 프로젝트 키 발급

프로젝트 설정의 **SDK Keys**에서 키를 만듭니다. 키는 한 번만 표시됩니다.

```bash
curl -s -X POST http://localhost:7780/api/v1/projects/octobooks/keys \
  -H "Authorization: Bearer $RHYMEHUB_TOKEN" -H 'Content-Type: application/json' \
  -d '{"name":"octobooks-ui-dev","allowedOrigins":["http://localhost:5173"]}'
# → {"key":"rhk_...", ...}
```

- 앱에는 **`rhk_` 프로젝트 키만** 넣습니다. **`rht_` API 토큰은 절대 넣지 마세요.** `VITE_*` 값은 번들에 그대로
  포함되어 누구나 볼 수 있습니다.
- `rhk_` 키로는 요청 작성과 자기가 보낸 요청 조회만 할 수 있습니다. 분당 30회로 제한됩니다.

### 7.2 SDK 패키지 준비

v0.1.0 SDK는 아직 npm에 올라가 있지 않습니다. rhymehub 저장소에서 tarball을 만들어 octobooks에 넣습니다.

```bash
mkdir -p ~/workspace/octobooks/vendor/rhymehub
cd ~/workspace/rhyme-hub
for p in packages/shared packages/sdk-typescript packages/sdk-react; do
  (cd "$p" && pnpm pack --pack-destination ~/workspace/octobooks/vendor/rhymehub)
done
ls ~/workspace/octobooks/vendor/rhymehub
# rhymehub-shared-0.1.0.tgz  rhymehub-sdk-0.1.0.tgz  rhymehub-react-0.1.0.tgz
```

octobooks **루트** `package.json`에 overrides를 추가합니다. `@rhymehub/react`는 `@rhymehub/sdk@0.1.0`에,
`@rhymehub/sdk`는 `@rhymehub/shared`에 의존하는데, npm에는 없으므로 로컬 파일로 연결해야 합니다.

```jsonc
// ~/workspace/octobooks/package.json
{
  "pnpm": {
    "overrides": {
      "@rhymehub/sdk": "file:vendor/rhymehub/rhymehub-sdk-0.1.0.tgz",
      "@rhymehub/shared": "file:vendor/rhymehub/rhymehub-shared-0.1.0.tgz"
    }
  }
}
```

이미 `pnpm` 필드가 있다면 그 안에 `overrides`만 합칩니다. 그다음 UI 패키지에 설치합니다.

```bash
cd ~/workspace/octobooks
pnpm --filter @branch/ui add file:../../vendor/rhymehub/rhymehub-react-0.1.0.tgz
```

`vendor/rhymehub/*.tgz`는 커밋해야 다른 환경과 rhymehub worktree(testCmd의 `pnpm install --frozen-lockfile`)에서도
설치됩니다.

### 7.3 환경 변수

`packages/ui/.env.local`을 만듭니다.

```bash
VITE_RHYMEHUB_PROJECT=octobooks
VITE_RHYMEHUB_KEY=rhk_여기에_발급받은_키
VITE_RHYMEHUB_ENDPOINT=http://127.0.0.1:7780
```

> ⚠️ octobooks의 `.gitignore`에는 현재 `.env.local`이 없습니다. 키가 커밋되지 않도록 먼저 다음을 추가하세요.
> ```
> .env.local
> *.local
> ```

### 7.4 Vite 프록시

브라우저가 같은 출처의 `/api`로 요청하고, Vite 개발 서버가 rhymehub로 전달하게 합니다. 그러면 CORS 설정이
필요 없고, 휴대폰에서 `*.ts.net:5173`으로 접속해도 동작합니다. octobooks UI는 현재 `/api` 경로를 쓰지 않으므로
충돌하지 않습니다.

```ts
// packages/ui/vite.config.ts
import { defineConfig, loadEnv } from 'vite';
// ... 기존 import 유지

export default defineConfig(({ mode }) => {
  const env = loadEnv(mode, process.cwd(), '');
  const proxy = {
    '/api': { target: env.VITE_RHYMEHUB_ENDPOINT || 'http://127.0.0.1:7780', changeOrigin: true },
  };
  return {
    plugins: [react(), tailwindcss(), pdfjsAssets()],
    define: { 'import.meta.env.VITE_APP_VERSION': JSON.stringify(version) },
    server: { host: '0.0.0.0', port: 5173, allowedHosts, proxy },
    preview: { host: '0.0.0.0', port: 4173, allowedHosts, proxy },
  };
});
```

> 프록시를 쓰지 않으려면 `endpoint`에 rhymehub 주소(예: `http://rich.tailf4a14e.ts.net:7780`)를 직접 넣고, 7.1의
> 키 `allowedOrigins`에 앱 출처를 모두 등록하세요. Electron 패키징 빌드처럼 Vite 서버가 없는 환경에서는 이
> 방법을 써야 합니다.

### 7.5 앱에 Provider와 위젯 추가

`packages/ui/src/main.tsx`에서 메인 앱(`<App/>`)만 감쌉니다. 발표 화면(`PresentAudienceApp`)이나 세션 창에는
위젯이 필요 없습니다. 키가 없으면 위젯을 렌더링하지 않도록 해서, 키가 없는 다른 개발 환경에서도 앱이 그대로
동작하게 합니다.

```tsx
// packages/ui/src/rhymehub.tsx
import type { ReactNode } from 'react';
import { RhymehubFeedback, RhymehubProvider } from '@rhymehub/react';

const projectId = import.meta.env.VITE_RHYMEHUB_PROJECT;
const projectKey = import.meta.env.VITE_RHYMEHUB_KEY;

export function WithRhymehub({ children }: { children: ReactNode }) {
  if (!projectId || !projectKey) return <>{children}</>;
  return (
    <RhymehubProvider
      options={{
        projectId,
        projectKey,
        endpoint: '', // 같은 출처의 /api → Vite 프록시 → rhymehub
        appVersion: import.meta.env.VITE_APP_VERSION,
        platform: 'web',
      }}
    >
      {children}
      <RhymehubFeedback />
    </RhymehubProvider>
  );
}
```

```tsx
// packages/ui/src/main.tsx (발췌)
import { WithRhymehub } from './rhymehub';

// ...
{presentWindowId ? (
  <PresentAudienceApp />
) : sessionWindowId ? (
  <SessionWindowApp />
) : (
  <WithRhymehub>
    <App />
  </WithRhymehub>
)}
```

진단 정보(콘솔 로그, 오류)는 **기본적으로 수집하지 않습니다**. 필요하면 `useRhymehubClient()`로 클라이언트를 받아
`client.diagnostics.captureConsole()` / `captureErrors()`를 명시적으로 켜세요. 개인정보가 많은 화면에서는 켜지
않는 것이 좋습니다.

그 밖에 쓸 수 있는 훅은 다음과 같습니다.

- `useRhymehubRequests()`: 이 설치에서 보낸 요청 목록과 진행 상태를 보여줍니다.
- `useRhymehubRequest(id)`: 요청 하나의 상태를 조회합니다.
- `useRhymehubUpdate()`: 새 버전 알림에 씁니다.

### 7.6 확인

```bash
cd ~/workspace/octobooks
pnpm dev     # @branch/ui 개발 서버
```

| 접속 | 주소 |
|---|---|
| 같은 Mac | http://localhost:5173 |
| LAN | http://192.168.219.108:5173 |
| Tailscale | http://rich.tailf4a14e.ts.net:5173 · http://100.102.77.120:5173 |

1. 화면의 피드백 버튼을 눌러 요청을 작성하고 보냅니다.
2. rhymehub 대시보드의 **Inbox**(`/inbox`)에 요청이 들어온 것을 확인합니다.
3. 요청에서 **작업 생성**을 누르면(`POST /requests/{id}/task`) 4장의 흐름이 그대로 시작됩니다.
   `autoTaskFromRequests`를 켜면 이 단계가 자동으로 진행됩니다. 자동 승인은 별개 설정이라 여전히 사람이 리뷰합니다.
4. 앱의 위젯에서 요청의 진행 상태가 표시됩니다.

---

## 8. API 토큰으로 자동화

개인 API 토큰은 **Settings → Tokens**에서 만듭니다(`POST /tokens`). `rht_...` 값은 한 번만 표시됩니다.
셸 환경 변수나 비밀 관리 도구에만 보관하고, 저장소나 앱 코드에는 넣지 마세요.

```bash
export RHYMEHUB=http://localhost:7780/api/v1
export RHYMEHUB_TOKEN=rht_xxx
H=(-H "Authorization: Bearer $RHYMEHUB_TOKEN" -H 'Content-Type: application/json')

curl -s "${H[@]}" $RHYMEHUB/projects                       # 프로젝트 목록
curl -s "${H[@]}" "$RHYMEHUB/tasks?projectId=octobooks"    # 작업 목록
curl -s "${H[@]}" $RHYMEHUB/tasks/<taskId>/events          # 이벤트 기록
curl -sN "${H[@]}" $RHYMEHUB/tasks/<taskId>/events/stream  # 실시간(SSE)
curl -s "${H[@]}" -X POST $RHYMEHUB/tasks/<taskId>/approve -d '{"release":false}'
~/workspace/rhyme-hub/bin/rhymehub status --server http://localhost:7780   # 요약
```

토큰을 쓰지 않고 브라우저 세션 쿠키로 상태를 바꾸는 요청을 보낼 때는 CSRF 헤더 `X-Rhymehub-CSRF: 1`이 필요합니다.
대시보드는 이 헤더를 자동으로 붙입니다.

---

## 9. 알림 (휴대폰)

1. 휴대폰에서 Tailscale 주소(`http://rich.tailf4a14e.ts.net:7780`)로 접속합니다.
2. 브라우저 메뉴에서 **홈 화면에 추가**해 PWA로 설치합니다.
3. **Settings → Notifications**에서 푸시 알림을 켭니다.

알림이 오는 경우:

- 질문
- 권한 요청
- REVIEW_READY
- 실패
- 릴리즈 완료

앱 안 알림 피드(`/notifications`)에서도 볼 수 있습니다.

> iOS 웹 푸시는 홈 화면에 설치한 PWA에서만 동작하며 HTTPS 출처가 필요합니다. 평문 HTTP 주소에서는 앱 안 피드만
> 사용할 수 있습니다.

---

## 10. 문제 해결

| 증상 | 원인 | 해결 |
|---|---|---|
| LAN 주소에서 `403 tls_required` | 평문 HTTP는 Tailscale/루프백만 허용 | 1.2 참고 (Tailscale 또는 TLS) |
| 작업이 바로 FAILED, 로그에 `command not found` | worktree에 `node_modules`가 없음 | testCmd 앞에 `pnpm install --frozen-lockfile &&` 추가 |
| `ERR_PNPM_OUTDATED_LOCKFILE` | lockfile과 package.json이 맞지 않음 | octobooks에서 `pnpm install` 후 lockfile을 커밋 |
| 작업이 WAITING_APPROVAL에서 멈춤 | 에이전트가 허용되지 않은 명령을 요청 | 작업 화면에서 허용 또는 거부 (4.4) |
| 권한을 허용했는데 계속 거부됨 | 요청한 규칙이 너무 넓거나 금지 목록에 있음 | 정상 동작입니다. 프롬프트로 다른 방법을 지시하세요 |
| 승인 시 `409` | 테스트가 실패했거나 상태가 REVIEW_READY가 아님 | 후속 지시로 고친 뒤 다시 리뷰 |
| 병합 실패 | 로컬 octobooks `main`에 커밋하지 않은 변경이 있거나 충돌 | 작업 디렉터리를 정리하고 재시도, 또는 방법 C 사용 |
| `claude: not found` / 인증 오류 | 서버 프로세스의 PATH에 claude가 없거나 로그인하지 않음 | `rhymehub doctor`, `claude` 로그인 확인 |
| SDK 요청이 `401`/`403` | 키가 틀렸거나 폐기됨, 또는 출처가 allowedOrigins에 없음 | 키 재발급, 프록시 사용 또는 allowedOrigins에 출처 추가 |
| SDK 요청이 `429` | 키당 분당 30회 제한 | 잠시 뒤 재시도 |
| 피드백 버튼이 안 보임 | `.env.local`이 없거나 개발 서버를 재시작하지 않음 | `VITE_*`를 확인하고 `pnpm dev` 재시작 |

서버 로그는 `serve`를 실행한 터미널에서 보거나, supervisor를 쓰는 경우 `~/.rhymehub/logs/`에서 확인합니다.

---

## 11. 보안 체크리스트

- [ ] 앱 코드와 `.env.local`에는 `rhk_` 키만 둡니다. `rht_` 토큰은 셸이나 비밀 관리 도구에만 둡니다.
- [ ] octobooks `.gitignore`에 `.env.local`을 추가했습니다.
- [ ] autoApprove와 autoRelease는 꺼 두고, 리뷰 후 사람이 승인합니다.
- [ ] 권한 요청은 무엇을 실행하려는지 확인하고 좁게 허용합니다.
- [ ] 서버를 외부에 열 때는 Tailscale 또는 TLS를 사용합니다. LAN 평문 허용(`insecureAllowedCIDRs`)은 신중히 씁니다.
- [ ] 다른 사용자에게는 필요한 최소 역할(reporter/developer)만 부여합니다.
- [ ] GitHub push는 diff를 확인한 뒤 직접 합니다.

자세한 보안 모델은 [SECURITY](SECURITY.md)를 보세요.
