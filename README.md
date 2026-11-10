<p align="center">
  <a href="https://gulpjs.com">
    <img height="257" width="114" src="https://raw.githubusercontent.com/gulpjs/artwork/master/gulp-2x.png">
  </a>
  <p align="center">The streaming build system</p>
</p>

[![Build Status][ci-image]][ci-url]

## What is gulp?

- **Automation** - gulp is a toolkit that helps you automate painful or time-consuming tasks in your development workflow.
- **Platform-agnostic** - A gulpfile is an ordinary Go program, so it runs anywhere Go runs.
- **Streaming files** - `Src`, `Dest` and `Symlink` stream vinyl file objects through your own transforms.
- **Simple** - By providing only a minimal API surface, gulp is easy to learn and simple to use.

## Installation

    go get github.com/gulpjs/gulp

The module needs Go 1.23 or newer and has no dependencies outside the standard library.

## Packages

| Package | Purpose |
| --- | --- |
| `github.com/gulpjs/gulp` | the task system: `Task`, `Series`, `Parallel`, `LastRun`, `Tree`, registries, plus `Src`, `Dest`, `Symlink` and `Watch` |
| `github.com/gulpjs/gulp/cli` | the `gulp` command line: `cli.Run` parses the flags, lists or runs tasks and logs their progress |
| `github.com/gulpjs/gulp/vfs` | the file system streams behind `Src`, `Dest` and `Symlink` |
| `github.com/gulpjs/gulp/vinyl` | the virtual file object passed through the streams |

## Sample `gulpfile.go`

A gulpfile is a `main` package. It registers tasks on a `gulp.Gulp` and hands the command line to `cli.Run`.

```go
package main

import (
	"os"

	"github.com/gulpjs/gulp"
	"github.com/gulpjs/gulp/cli"
	"github.com/gulpjs/gulp/vfs"
	"github.com/gulpjs/gulp/vinyl"
)

// copyTask streams files from src to dest and reports completion through done.
func copyTask(g *gulp.Gulp, glob, out string) gulp.TaskFunc {
	return func(done gulp.Done) {
		files, err := g.Src(glob, vfs.Options{"sourcemaps": true})
		if err != nil {
			done(err)
			return
		}
		rename := vfs.Through(func(f *vinyl.File) (*vinyl.File, error) {
			return f, f.SetExtname(".min.js")
		})
		target, err := g.Dest(out, vfs.Options{"sourcemaps": "."})
		if err != nil {
			done(err)
			return
		}
		go func() {
			_, err := files.Pipe(rename).Pipe(target).Wait()
			done(err)
		}()
	}
}

func main() {
	g := gulp.New()
	tasks := []struct {
		name string
		task *gulp.Task
	}{
		{"clean", &gulp.Task{Fn: func(done gulp.Done) { done(os.RemoveAll("assets")) }}},
		{"scripts", &gulp.Task{Fn: copyTask(g, "src/scripts/**/*.js", "assets/scripts/"), Description: "Copy scripts"}},
		{"styles", &gulp.Task{Fn: copyTask(g, "src/styles/**/*.css", "assets/styles/")}},
	}
	for _, t := range tasks {
		if err := g.Task(t.name, t.task); err != nil {
			panic(err)
		}
	}
	both, err := g.Parallel("scripts", "styles")
	if err != nil {
		panic(err)
	}
	build, err := g.Series("clean", both)
	if err != nil {
		panic(err)
	}
	if err := g.Task("default", build); err != nil {
		panic(err)
	}
	os.Exit(cli.Run(g, "gulpfile.go", os.Args[1:]))
}
```

Run it with `go run . --tasks`, `go run . build` or `go run . --help`. The flags are the ones the gulp CLI has
always had: `--tasks`, `--tasks-simple`, `--tasks-json`, `--depth`, `--compact-tasks`, `--sort-tasks`, `--series`,
`--continue`, `--silent`, `--log-level`, `--color`, `--no-color`, `--cwd`, `--gulpfile`, `--completion`,
`--version` and `--help`. Default flags can be set in a `.gulp.json` file found upward from the working folder
(`{"flags": {"continue": true}}`).

## Incremental Builds

You can filter out unchanged files between runs of a task using the `since` option of `Src` and `LastRun`:

```go
images := &gulp.Task{}
images.Fn = func(done gulp.Done) {
	opts := vfs.Options{}
	if last, ok, err := g.LastRun(images, 1); err == nil && ok {
		opts["since"] = float64(last)
	}
	files, err := g.Src("src/images/**/*.{jpg,jpeg,png}", opts)
	if err != nil {
		done(err)
		return
	}
	target, err := g.Dest("build/img/", nil)
	if err != nil {
		done(err)
		return
	}
	go func() {
		_, err := files.Pipe(target).Wait()
		done(err)
	}()
}
```

Task run times are saved in memory and are lost when the program exits. They save time while a `Watch` keeps the
program running.

## Development

    go build ./...   # build every package
    go test ./...    # run the tests
    make             # gofmt check, go vet and go test

## Want to contribute?

Anyone can help make this project better - check out our [Contributing guide](/CONTRIBUTING.md)!

[ci-url]: https://github.com/gulpjs/gulp/actions?query=workflow:dev
[ci-image]: https://img.shields.io/github/actions/workflow/status/gulpjs/gulp/dev.yml?branch=master&style=flat-square
