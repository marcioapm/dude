# Trying images in a conversation, locally, with a real agent

What this exercises: a person pastes a screenshot into a steer, dude scales
it in the browser, uploads it to MinIO, the orchestrator reads it back and
sends it to lux on the steer's input, and a real OpenCode agent (through
llm-proxy) gets it as an image in its next model step.

It runs the stack the README calls "Running the whole thing", with two
additions: a MinIO for the images, and lux built from `feat/input-attachments`.
Nothing here is automated: `tests/demo.py` runs the fake lux only, and the
contract test (`tests/suites/test_lux_contract.py::test_an_image_steer_and_an_image_prompt_on_real_lux`,
`run_tests.py --lux`) checks the wire, not a model.

## 0. What you need

- This branch (`feat/chat-images`) built: `bun install`, then
  `(cd orchestrator && go build -o bin/ ./cmd/...)`.
- lux checked out at `feat/input-attachments` (`~/git/lux`).
- Docker, Bun ≥ 1.4, Go, `uv`, Chrome.
- An llm-proxy URL and key whose models see images (every model dude
  configures does). Its request body limit must take a few MB: one image is
  sent base64 inside the model request.

## 1. Postgres and MinIO

```bash
docker run -d --name dude-postgres \
  -e POSTGRES_USER=dude -e POSTGRES_PASSWORD=dude -e POSTGRES_DB=dude \
  -p 5433:5432 pgvector/pgvector:pg18
DATABASE_URL="postgres://dude:dude@localhost:5433/dude" bun run migrate

docker run -d --name dude-minio -p 127.0.0.1:9000:9000 \
  -e MINIO_ROOT_USER=dudes3 -e MINIO_ROOT_PASSWORD=dudes3-secret \
  minio/minio server /data
# A bucket for photos and attached images
docker run --rm --network host --entrypoint sh minio/mc -c \
  'mc alias set l http://127.0.0.1:9000 dudes3 dudes3-secret && mc mb l/dude-images'
```

(versitygw works the same, as the test suite uses it:
`versity/versitygw:v1.7.0 --port :9000 posix /tmp` with
`ROOT_ACCESS_KEY`/`ROOT_SECRET_KEY`, and a bucket made with any S3 client.)

The S3 settings, used by **both** processes from now on:

```bash
export DUDE_S3_BUCKET=dude-images
export DUDE_S3_ENDPOINT=http://127.0.0.1:9000
export DUDE_S3_REGION=us-east-1
export DUDE_S3_ACCESS_KEY=dudes3
export DUDE_S3_SECRET_KEY=dudes3-secret
```

## 2. lux, from `feat/input-attachments`, with the runtime image

```bash
(cd ~/git/lux && git fetch && git checkout feat/input-attachments)
scripts/runtime-image.sh dude-runtime:dev
(cd ~/git/lux/tests && uv run python run_tests.py --serve --detach --image dude-runtime:dev)
# prints luxd_url and api_key; keep them
```

lux's hosts must reach llm-proxy (the Run's egress allows `DUDE_LLM_URL`'s
host).

## 3. The orchestrator

```bash
DATABASE_URL="postgres://dude_app:dude_app@localhost:5433/dude" \
DUDE_ORCHESTRATOR_TOKEN=dev-token \
LUX_URL=<luxd_url> LUX_API_KEY=<api_key> \
DUDE_AGENT_IMAGE=docker.io/library/dude-runtime:dev \
DUDE_LLM_URL=<llm-proxy base URL, e.g. https://llmproxy.example.com/v1> \
DUDE_LLM_KEY=<llm-proxy key> \
  orchestrator/bin/dude-orchestrator
```

It must have the `DUDE_S3_*` variables of step 1: it reads each image from
the bucket to send it to lux. Without them a steer with an image fails with
"its images could not be read: image storage is not configured (s3.bucket)".

## 4. The backend, a person, the web app

```bash
DATABASE_URL="postgres://dude_app:dude_app@localhost:5433/dude" \
DUDE_ORCHESTRATOR_URL=http://127.0.0.1:3100 DUDE_ORCHESTRATOR_TOKEN=dev-token \
  bun run dev                                   # :3000, with the DUDE_S3_* variables

OWNER_DSN="postgres://dude:dude@localhost:5433/dude" \
DATABASE_URL="postgres://dude_app:dude_app@localhost:5433/dude" \
  bun run scripts/seed-github.ts                 # or seed-demo.ts; prints a userKey

(cd apps/web && bun run dev)                     # :5180
```

Give the project a real model for the implementer, in its settings (Agents)
or through the API:

```bash
curl -X PATCH localhost:3000/v1/projects/<projectId> \
  -H "authorization: Bearer <userKey>" -H "content-type: application/json" \
  -d '{"agentModels":{"implementer":{"model":"llm-anthropic/claude-sonnet-5"},
       "reviewer":{"model":"llm-anthropic/claude-sonnet-5"},
       "simplifier":{"model":"llm-anthropic/claude-sonnet-5"}}}'
```

## 5. Paste an image into a steer

1. Open http://localhost:5180, sign in with the `userKey`.
2. New task, e.g. "Describe what you are shown", goal "When you are sent an
   image, describe it in one paragraph before anything else." **Create and
   deliver.** (You can attach an image here too: it goes to the first agent
   with the task, as `workload.attachments`.)
3. Open the Implement session (Sessions tab). While it works:
   - Take a screenshot (any window), click in **Steer the agent…**, press
     Ctrl+V. A chip appears with a ring while it uploads; its size badge
     shows when it is up. The 📎 does the same through a file picker;
     dragging a file over the conversation shows "Drop to attach…".
   - Type "What is in this screenshot?" and press Enter.

What to expect:

- The steer turn shows the image under the text. Its header goes from
  Queued to "sent … · read hh:mm:ss, after <tool>" when the agent's next step
  reads it — words and image together; lux reports one receipt for both.
- The agent's next message describes the screenshot. Nothing in dude claims
  it understood: judge from the reply.
- Click the image: the viewer says "The agent got W×H TYPE · size, scaled
  from W×H · size · read at hh:mm" (no "scaled from" for an image already
  ≤ 2000 px); "Original W×H" shows what you pasted; Download saves it.
- In lux, the Run's `lux.input` record for the steer carries
  `attachments: [{name, contentType, size, sha256}]`, and the image is also
  at `$LUX_INPUTS/<directive id>/1-<name>` in the container.
- Answer a question the same way: the composer turns to Answer with the same
  tray.

Things to try that should be refused in place, with Steer staying off until
removed: a PDF ("PDF not supported"), a file over 10 MB ("NN MB · max 10"),
a seventh image ("max 6").

If the agent's turn ends with "the agent does not take images", the lux
adapter in use did not advertise image input (an ACP agent other than
OpenCode); the steer shows "Not delivered" with Retry, and the image stays
with it.

## 6. Clean up

```bash
docker rm -f dude-minio dude-postgres
```

Stop the detached lux environment the way lux's `tests/README` says for
`run_tests.py --serve --detach`.

An upload never sent is deleted after 24 hours, and a task's images with
the task; the backend's sweeper deletes their objects (every 10 minutes).
