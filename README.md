# SPT-Progress

Read-only website for an SPT (Single Player Tarkov) server: every launcher
account's PMC progress at a glance — level, experience, stash money by
currency (₽/$/€), quest counts, Fence rep, raid stats and top skills, read
straight from the server's `user/profiles/*.json`. No database, no writes:
mount the profiles dir read-only and go.

Pairs with [GameCTL](https://github.com/GameCTL-HQ/GameCTL)-deployed SPT/Fika
servers (see `k8s/spt-progress.yaml`) and publishes to the internet with
[ProxyCTL](https://github.com/GameCTL-HQ/ProxyCTL).

## Config (env)

| Var | Default | Meaning |
| --- | --- | --- |
| `GAMECTL_PROFILES_DIR` | `/data/profiles` | SPT `user/profiles` directory |
| `SITE_BRAND` | `GameCTL` | Owner name in the title/header/footer |
| `LISTEN_ADDR` | `:8080` | HTTP listen address |

Image: `ghcr.io/gamectl-hq/spt-progress` (built by Actions on every push).
