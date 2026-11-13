# PokeVend Postman collections

| File | What it covers |
| --- | --- |
| `vending-server.postman_collection.json` | **Vending server API**: the admin API (`/api/admin`, Bearer token) and the machine API (`/api/machine`, `X-Machine-Key`). 55 requests in 12 folders |
| `vending-machine-agent.postman_collection.json` | **Kiosk Agent API**: the local API the agent serves to the kiosk page (`/api/...` on 127.0.0.1). 14 requests in 3 folders |
| `PokeVend-GCP-VM.postman_environment.json` | Server `https://35.219.98.32`, agent `http://127.0.0.1:8080` (on the machine's PC) |
| `PokeVend-Local-Mac.postman_environment.json` | Server `http://localhost:8080`, agent `http://127.0.0.1:8090` (both on your Mac) |

## Use

1. In Postman: **Import** → drop all four files.
2. Pick an environment (top right) and set **`machineKey`** to an active machine's key: `./deploy/deploy.sh USER@35.219.98.32 keys` for the VM, or the `seed-demo` output locally. Use VM-02's key on a laptop and keep VM-01's for the real machine.
3. **Vending server API**: run folders 0 to 10 in order with the Collection Runner. Each request saves what the next one needs (tokens, ids, the catalog version, the slot to buy from). Folder 11 deletes all data and reloads the demo; run it only on purpose (needs `DEMO_MODE=true`).
4. **Kiosk Agent API**: run it on the same computer as the agent, since the agent listens on 127.0.0.1 only. With `"board": "serial"`, the request `A2 · Pay` turns a real coil; use `"board": "sim"` while testing. Switch the simulator (OK, Jam, No echo, power cut) on the agent's `/dev/` page.

The demo logins in the environments (`admin`, `budi`) are the seed-demo accounts. Change them if your server uses other accounts.

## What the runs leave behind

One sale, one cancelled and one failed transaction on the slot with the most stock (the failed one blocks the slot until the restock visit in folder 9 clears it), one restock visit, a new product "Gacha Postman", a new machine `VM-PMxxxx` with 8 slots, and a new operator `pmxxxxx`. All of it shows in the admin web and the change log.
