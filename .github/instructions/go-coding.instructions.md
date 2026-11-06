---
name: Go Coding Guidelines
description: Naming conventions and style rules for Go source files
applyTo: '**/*.go'
---

# Go coding guidelines

## Variable naming

Use descriptive variable names. Avoid short, cryptic names — they make
code harder to read and search.

### Allowed short names (1–2 characters)

- Loop indices: `i`, `j`, `k`
- `ok` for comma-ok idioms (`val, ok := m[key]`)
- `err` for errors
- `ctx` for `context.Context`
- `t`, `b` for `*testing.T` / `*testing.B`
- `tt` for table-driven test entries (`for _, tt := range tests`)
- Method receivers: 1–2 letter abbreviation of the type (e.g. `s` for
  `Store`), per Go convention

### Common 3-letter abbreviations (acceptable)

These are universally understood in Go and may be used:
`cmd`, `buf`, `dir`, `req`, `res`, `out`, `src`, `dst`, `arg`, `msg`

### Everything else must be descriptive

This applies everywhere: local variables, function parameters, return
values, **and range-loop variables** iterating over collections.

```go
// Bad
s := store.New(db)
p := s.GetProject(id)
det := git.New()
rec := httptest.NewRecorder()
ev := stream.Read()

// Good
projectStore := store.New(db)
project := projectStore.GetProject(projectID)
detector := git.New()
recorder := httptest.NewRecorder()
event := stream.Read()
```

Range-loop variables must describe the element, not abbreviate it:

```go
// Bad
for _, p := range projects { ... }
for _, ev := range events { ... }

// Good
for _, project := range projects { ... }
for _, event := range events { ... }
```

HTTP handler parameters must be spelled out:

```go
// Bad
func handle(w http.ResponseWriter, r *http.Request) { ... }

// Good
func handle(writer http.ResponseWriter, request *http.Request) { ... }
```
