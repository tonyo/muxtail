# muxtail

Tail multiple files simultaneously, with labeled and colored output.

```
2026-03-22T14:05:01 [api] GET /health 200
2026-03-22T14:05:01 db.log: SELECT 1
2026-03-22T14:05:02 [api] POST /login 401
```

## Usage

```
muxtail [flags] [FILE ...]
```

Pass `-` or omit files to read from stdin.

## Flags

| Flag | Short | Default | Description |
|------|-------|---------|-------------|
| `--lines` | `-n` | `10` | Initial lines to show per file |
| `--follow` | `-f` | off | Follow files for new lines |
| `--follow-retry` | `-F` | off | Follow and retry if file is missing/recreated |
| `--prefix` | `-p` | `none` | Auto-label mode: `none`, `basename`, `fullname` |
| `--label` | `-l` | — | Per-file label (repeatable, positional) |
| `--ts` | `-T` | off | Prepend each line with a timestamp |
| `--no-color` | — | off | Disable colored labels |

## Examples

**Tail two files with auto labels:**
```
muxtail -p basename app.log db.log
```

**Follow with custom labels:**
```
muxtail -f -l "[api] " -l "[db] " app.log db.log
```

**Timestamps + follow:**
```
muxtail -Tf app.log
```

**Disable colors (e.g. when piping):**
```
muxtail --no-color -p basename app.log db.log | grep ERROR
```
Colors are also suppressed automatically when stdout is not a terminal.

**Tail stdin:**
```
kubectl logs -f my-pod | muxtail --ts -l "[pod] "
```

## Label convention

Labels are written verbatim before each line — include your own spacing and brackets (e.g. `"[db] "`). `--prefix` auto-generates labels with a trailing `: ` (e.g. `app.log: `). `--label` takes priority over `--prefix` for each positional file.

## Install

```
go install muxtail@latest
```

Or build from source:

```
go build -o muxtail .
```
