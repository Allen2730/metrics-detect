package logger

import (
	"os"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

var global *zap.Logger

func Init(level, file string) error {
	lvl := zapcore.InfoLevel
	if err := lvl.UnmarshalText([]byte(level)); err != nil {
		lvl = zapcore.InfoLevel
	}

	encCfg := zap.NewProductionEncoderConfig()
	encCfg.TimeKey = "time"
	encCfg.EncodeTime = zapcore.ISO8601TimeEncoder

	var cores []zapcore.Core

	// 控制台输出
	consoleEnc := zapcore.NewConsoleEncoder(encCfg)
	cores = append(cores, zapcore.NewCore(consoleEnc, zapcore.AddSync(os.Stdout), lvl))

	// 文件输出
	if file != "" {
		f, err := os.OpenFile(file, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
		if err == nil {
			fileEnc := zapcore.NewJSONEncoder(encCfg)
			cores = append(cores, zapcore.NewCore(fileEnc, zapcore.AddSync(f), lvl))
		}
	}

	global = zap.New(zapcore.NewTee(cores...), zap.AddCaller())
	return nil
}

func Get() *zap.Logger {
	if global == nil {
		global, _ = zap.NewDevelopment()
	}
	return global
}

func Info(msg string, fields ...zap.Field)  { Get().Info(msg, fields...) }
func Warn(msg string, fields ...zap.Field)  { Get().Warn(msg, fields...) }
func Error(msg string, fields ...zap.Field) { Get().Error(msg, fields...) }
func Debug(msg string, fields ...zap.Field) { Get().Debug(msg, fields...) }
func Sync()                                 { _ = Get().Sync() }
