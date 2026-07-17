# Contributing to Open Markets

Thanks for your interest in contributing. This project is a Cities: Skylines 1 (the original — not CS2)
code mod plus an optional, self-hosted Go backend for the online "play with friends" league economy.
Issues and PRs are welcome: bug reports, compatibility findings, balance feedback, and crisis-bank ideas
especially.

---

## Platform constraints (non-negotiable)

The mod (`OpenMarkets/`) targets **`net35`** because Cities: Skylines 1 runs on **Unity 5.6 / Mono**, which
only has a .NET Framework 3.5-equivalent BCL. This shapes what C# you can use:

- **Target framework:** `net35`. `LangVersion 9` *syntax* is fine, but **only** where the underlying API
  exists in net35 — newer language features that lean on newer BCL types will not compile or will fail at
  runtime under Mono.
- **Never use C# 9 `record`** — it is uncompilable on net35.
- **Avoid `async`/`await`** — there is no `Task` in the net35 BCL. All networking uses `UnityWebRequest`
  with callbacks/polling instead.
- **Avoid `init`, `Index`/`Range`, and value-tuples** unless a polyfill (or `System.ValueTuple`) is
  deliberately added — don't assume they work.
- **LINQ-to-objects and string interpolation are fine** (`System.Core` is present on net35).

## The Harmony pattern

The mod patches the game with [Harmony](https://github.com/pardeike/Harmony) via the
[`CitiesHarmony.API`](https://github.com/boformer/CitiesHarmony) NuGet package (the "boformer pattern"),
**not** a bundled Harmony DLL:

- Keep **all** `HarmonyLib` references out of `Mod.cs` / the `IUserMod` entry point. Patch from a dedicated
  `Patcher` / `Patches` class instead, so a missing or late Harmony install never blocks the mod from
  loading.
- Gate all patching on `HarmonyHelper.DoOnHarmonyReady(...)`.
- **Prefer `Prefix`/`Postfix` patches** — they're additive and tolerate other mods patching the same method.
  **Avoid transpilers** unless there's no other way.
- Patch methods are `static`. Resolve private or overloaded targets with `TargetMethod()` and
  `AccessTools.Method(type, name, paramTypes)`; reach private fields/methods via `AccessTools`/`Traverse`
  (CS1 has no publicizer tooling).
- Make patch/unpatch **idempotent**, and unpatch in `OnDisabled` guarded by
  `HarmonyHelper.IsHarmonyInstalled`.
- **Never** ship a bundled `CitiesHarmony.Harmony.dll` — only `OpenMarkets.dll` + `CitiesHarmony.API.dll`
  should be published.

## Lifecycle & threading

- `IUserMod` is the entry point; `LoadingExtensionBase` handles level load/unload;
  `ThreadingExtensionBase` (`OnBefore/AfterSimulation*`) runs on the **simulation thread**;
  `ISerializableDataExtension` (`OnLoadData`/`OnSaveData`) also runs on the **simulation thread**.
- These hooks re-fire on enable/disable/level-load/recompile — never assume single-fire. Make setup and
  teardown reversible.
- **Thread safety matters:** `TransferManager` offer APIs and simulation data must only be touched from the
  **simulation thread**; Unity/UI objects only from the **main thread**. Marshal across with
  `QueueSimulationThread` / `QueueMainThread`.
- **Never** allocate or log in hot paths (per-frame, per-tick, per-transfer code). Wrap patch bodies in
  try/catch.

## Save-safety

- Persist only under the mod's own versioned data id via `ISerializableDataExtension`. Version the blob,
  keep it append-only, and guard reads with try/catch that fall back to clean defaults.
- **Removing the mod must always leave the save loadable.** Never mutate vanilla manager data in a way the
  base game can't deserialize without the mod present.

---

## Building the mod

You only need a modern **.NET SDK** (8+) — no Windows, no Mono, and no .NET Framework install required. The
mod targets `net35`, but a NuGet reference-assembly package
(`Microsoft.NETFramework.ReferenceAssemblies.net35`) supplies the net35 BCL, and `CitiesHarmony.API`
supplies Harmony — both restore automatically. The build itself is cross-platform (Windows / macOS /
Linux).

```bash
dotnet build OpenMarkets.sln -c Debug
```

### The one external dependency: your game's `Managed` DLLs

The build needs four **copyrighted game assemblies** that can't be redistributed, so there's no public
package for them: `ICities.dll`, `ColossalManaged.dll`, `Assembly-CSharp.dll`, `UnityEngine.dll`. Two ways
to provide them:

- **Install Cities: Skylines 1** (a native macOS / Windows / Linux Steam game). `Directory.Build.props`
  already has the per-OS Steam `Managed` paths baked in, so `dotnet build` works with zero extra config.
- **Or copy just those 4 files** from any existing install into a folder and point the build at it: copy
  `Directory.Build.props.user.example` to `Directory.Build.props.user` (gitignored) and set
  `<ManagedDir>/path/to/Managed</ManagedDir>`, or pass `-p:ManagedDir=...` on the command line.

The build fails fast with a clear message if the path is wrong, and auto-deploys the built DLL to the
game's Mods folder (`ModsDir`).

Changes that affect runtime behavior (money booking, cargo traffic, UI) need an **in-game** check — building
cleanly is necessary but not sufficient.

---

## Running the server

The optional online backend (`server/`) is a small, dependency-free Go service.

```bash
cd server
make run          # listens on :8080, persists to ./data/openmarkets.json
```

See [`docs/RUNNING-THE-SERVER.md`](docs/RUNNING-THE-SERVER.md) for the full configuration reference,
the operator console, and production deployment notes, and [`server/QUICKSTART.md`](server/QUICKSTART.md)
for a guided walkthrough.

Before submitting a server change, run the full pre-merge gate:

```bash
cd server
make verify       # fmt-check, vet, race tests, economy invariant sweep, money-math fuzz
```

---

## Publishing / Workshop

If you're preparing a release (Workshop listing, changelog entry, etc.), see
[`docs/WORKSHOP.md`](docs/WORKSHOP.md) for the Steam Workshop description template.

---

## Code review expectations

- Keep functions small and files focused; extract shared logic instead of duplicating it.
- Handle errors explicitly — never swallow them silently.
- No hardcoded secrets, credentials, or tokens.
- New functionality should come with tests where practical (Go: table-driven tests under `server/internal/`;
  the C# client is harder to unit-test in isolation, so favor documented manual/in-game verification steps
  in the PR description).
- Match the surrounding code's style rather than introducing a new convention.

## Versioning & releases

Open Markets follows [Semantic Versioning](https://semver.org) — `vMAJOR.MINOR.PATCH`:

- **MAJOR** — breaking changes to the client/server API or the save-blob format (an old client and a new server, or vice versa, may not interoperate).
- **MINOR** — new, backward-compatible features.
- **PATCH** — bug fixes and balance tweaks.

Each public release is an **annotated git tag** (`git tag -a vX.Y.Z -m "…"`) pushed to the repo, paired with a matching GitHub Release and Steam Workshop changelog entry. When the client/server protocol changes, bump MAJOR and state the minimum compatible server version in the release notes, so self-hosters know when they must update. Keep the three in sync: tag, GitHub Release, Workshop changelog.

## Design background

For the "why" behind design decisions, see [`DESIGN.md`](./DESIGN.md) (mod design rationale) and
[`BACKEND.md`](./BACKEND.md) (server architecture). [`docs/ARCHITECTURE.md`](./docs/ARCHITECTURE.md) ties
the client and server together, and [`CHANGELOG.md`](./CHANGELOG.md) has the project history.
