package router

// Version is the version of the router itself. An application logs it to
// record which router a build carries, which otherwise takes a walk through
// [runtime/debug.ReadBuildInfo]:
//
//	slog.Info("starting", slog.String("router", router.Version))
const Version = "0.1.0"
