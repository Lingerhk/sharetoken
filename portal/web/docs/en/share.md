# Share a model

Share a model on your machine with the network and earn credit for the tokens actually served. You do not need a public IP or open ports.

> [!WARNING]
> **Running a model requires decrypting the request, so your machine can see the prompts that others send.** Do not log or store them. Share only models that run on your own machine and that you have the right to share.

## Step 1: Prepare a model server {#server}

By default the client works with Ollama on your machine. For other OpenAI-compatible model servers, see [Use another model server](#other-server).

::: macos
```bash
brew install ollama
brew services start ollama
ollama pull gemma4:e4b
```

You can also download the app from [ollama.com](https://ollama.com/download). Once opened, it keeps running in the background.
:::

::: linux
```bash
curl -fsSL https://ollama.com/install.sh | sh
ollama pull gemma4:e4b
```

The install script registers Ollama as a systemd service that starts at boot.
:::

::: windows
```powershell
winget install Ollama.Ollama
ollama pull gemma4:e4b
```

You can also download the installer from [ollama.com](https://ollama.com/download). It runs in the notification area and starts when you log in to Windows.
:::

Check that the client can see the model:

```bash
{{exe}} share detect
```

## Step 2: Add a model {#add}

```bash
{{exe}} share add gemma4:e4b -id gemma-4-e4b
```

| Option | Meaning | Default |
|---|---|---|
| `-id` | The model's ID on the network. Buyers use it to call the model | Same as the local model name |
| `-in` | Price per million input tokens, in micro-credit | `100000` (0.1 credit) |
| `-out` | Price per million output tokens, in micro-credit | `200000` (0.2 credit) |

> [!TIP]
> If other nodes already share the same model, use the same ID so buyers can select you together with them. Run `{{exe}} models` first to see the existing IDs and prices. The coordinator picks nodes by price, reputation, latency and success rate, and price has the highest weight.

Adding a model turns sharing on automatically.

## Step 3: Go online and take jobs {#up}

```bash
{{exe}} up
```

If `up` is already running, `share` commands take effect immediately, with no restart. To take jobs over a long time, [run in the background](/docs/running#background).

Check the status:

```bash
{{exe}} status
```

If you see `sharing 1 model(s): ...`, you are online. If there is an `offer not accepted: ...` line, it gives the reason for the rejection.

## Check earnings {#earnings}

```bash
{{exe}} balance
{{exe}} jobs -role sell -n 20
```

- **Settlement:** A job is settled after the buyer confirms it (Ack). A job the buyer does not confirm settles automatically after the 24-hour dispute window. So earnings may show up in `pending` first.
- **Fee:** The platform takes 5% of each job.
- The portal's [jobs](/app/#/jobs) and [ledger](/app/#/ledger) pages have the details.

## Manage sharing {#manage}

| Action | Command |
|---|---|
| View settings | `{{exe}} share` |
| List local models | `{{exe}} share detect` |
| Change a price (add the same ID again) | `{{exe}} share add gemma4:e4b -id gemma-4-e4b -in 80000 -out 150000` |
| Remove a model | `{{exe}} share rm gemma-4-e4b` |
| Pause taking jobs | `{{exe}} share off` |
| Resume taking jobs | `{{exe}} share on` |

## Use another model server {#other-server}

Any local service with an OpenAI-compatible interface works, such as LM Studio, vLLM or llama.cpp:

```bash
{{exe}} share upstream http://127.0.0.1:1234/v1
{{exe}} share detect
{{exe}} share add my-local-model -id my-model
```

If the model server needs an API key, put the key in an environment variable and write the variable's name in `share.upstream_key_env` in the [configuration file](/docs/config#share).

## Concurrency and performance {#tuning}

The number of requests handled at the same time is 2 by default, set by `share.concurrency` in the configuration file. Adjust it to what your machine can handle, then run this command to apply the change:

```bash
{{exe}} share on
```

## How cheating is prevented {#audit}

The coordinator places random jobs under a new identity and recomputes them on a reference model to compare. If two checks in a row do not match, the node is paused from taking jobs for 24 hours. Make sure the model you share matches its ID, and do not overstate usage: buyers recompute the hash themselves and can get a refund with the evidence.
