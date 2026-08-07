This is fork of [wailsapp/go-webview2](https://github.com/wailsapp/go-webview2) Because it seems the package is now being maintained as private under wails v3.

## This fork declares its own module path

`module github.com/13thgoutham/go-webview2`, renamed from upstream's path on **`main` only**. Consumers
therefore require it directly:

```
require github.com/13thgoutham/go-webview2 v1.0.25
```

No `replace` directive is needed or wanted. `replace` means "temporarily substitute someone else's
module", and neither half of that is true here: upstream's package moved into the Wails v3 monorepo
under `internal/` (which cannot be imported), so it is not accepting patches and this fork is not
temporary.

**The upstream PR branches deliberately keep the original module path**, because upstream can only
accept patches against its own path. Two consequences, and getting either wrong silently breaks a PR:

- **Never rebase or merge `main` into a PR branch** (`pr/generator-first`, and the minimal-patch
  branch behind wailsapp/go-webview2#36). That would drag the rename in and make the diff unmergeable.
- **Port fixes branch-ward, not main-ward.** Author on `main`, then cherry-pick onto the PR branch and
  revert the import paths in that commit -- 15 files, mechanical. Cherry-picking the other direction
  puts the upstream path back on `main`.

`scripts/` contains no module path, so a regeneration cannot revert the rename. That was checked
rather than assumed, because output-only fixes reverting on regeneration is the exact defect this
fork exists to correct.


----------------


This is a locally maintained fork of [go-webview2](https://github.com/jchv/go-webview2) 
that is intended to be used with Wails applications. It is not intended to be used
as a standalone package.

----------------

To update this package, run the following commands:

```bash
task update
```

----------------

Original README.md follows:

# go-webview2

This is a proof of concept for embedding Webview2 into Go without CGo. It is based
on [webview/webview](https://github.com/webview/webview) and provides a compatible API.

## Notice

Because this version doesn't currently have an EdgeHTML fallback, it will not work unless you have a Webview2 runtime
installed. In addition, it requires the Webview2Loader DLL in order to function. Adding an EdgeHTML fallback should be
technically possible but will likely require much worse hacks since the API is not strictly COM to my knowledge.

## Demo

For now, you'll need to install the Webview2 runtime, as it does not ship with Windows.

[WebView2 runtime](https://developer.microsoft.com/en-us/microsoft-edge/webview2/)

After that, you should be able to run go-webview2 directly:

```
go run go-webview2/cmd/demo
```

This will use go-winloader to load an embedded copy of WebView2Loader.dll.

If this does not work, please try running from a directory that has an appropriate copy of `WebView2Loader.dll` for your
GOARCH. If _that_ worked, *please* file a bug so we can figure out what's wrong with go-winloader :)
