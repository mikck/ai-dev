package main

import (
	"bytes"
	"context"
	"embed"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"regexp"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"
)

//go:embed public/*
var frontend embed.FS

var proofPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)

func router(s *store, secret string) http.Handler {
	r := gin.New()
	r.Use(gin.Recovery())
	r.Use(func(c *gin.Context) {
		c.Header("X-Content-Type-Options", "nosniff")
		c.Next()
	})
	for route, file := range map[string]string{"/": "index.html", "/app.js": "app.js", "/app.css": "app.css"} {
		name := file
		r.GET(route, func(c *gin.Context) {
			data, err := frontend.ReadFile("public/" + name)
			if err != nil {
				c.String(404, "Build the frontend before packaging ai-dev")
				return
			}
			http.ServeContent(c.Writer, c.Request, name, time.Time{}, bytes.NewReader(data))
		})
	}
	get := func(path string, call func(*gin.Context) (any, error)) {
		r.GET(path, func(c *gin.Context) {
			data, err := call(c)
			if err != nil {
				c.JSON(404, input{"error": err.Error()})
				return
			}
			c.JSON(200, data)
		})
	}
	get("/api", func(_ *gin.Context) (any, error) { return s.overview() })
	get("/api/workspaces/:id/reviews", func(c *gin.Context) (any, error) { return s.reviewInbox(c.Param("id")) })
	get("/api/executors", func(_ *gin.Context) (any, error) { return s.executors() })
	get("/api/workspaces/:id/setup", func(c *gin.Context) (any, error) { return s.setup(c.Param("id")) })
	get("/api/drafts/:id", func(c *gin.Context) (any, error) { return s.getDraft(c.Param("id")) })
	get("/api/requirements/:id", func(c *gin.Context) (any, error) { return s.item(c.Param("id")) })

	post := func(path string, status int, action func(*gin.Context, input) (any, error)) {
		r.POST(path, func(c *gin.Context) {
			if c.ContentType() != "application/json" {
				c.JSON(415, input{"error": "需要 application/json"})
				return
			}
			c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 524288)
			var data input
			if err := c.ShouldBindJSON(&data); err != nil || data == nil {
				c.JSON(400, input{"error": "请求 JSON 无效"})
				return
			}
			result, err := action(c, data)
			if c.Writer.Written() {
				return
			}
			if err != nil {
				c.JSON(400, input{"error": err.Error()})
				return
			}
			c.JSON(status, result)
		})
	}
	for name, action := range map[string]func(input) (string, error){
		"workspaces": s.workspace, "repositories": s.repository, "requirements": s.requirement,
		"resources": s.resource, "sops": s.saveSOP, "drafts": s.draft,
	} {
		call := action
		post("/api/"+name, 201, func(_ *gin.Context, data input) (any, error) { id, err := call(data); return input{"id": id}, err })
	}
	post("/api/sops/publish", 201, func(_ *gin.Context, data input) (any, error) {
		version, err := s.publishSOP(data)
		return input{"version": version}, err
	})
	for name, action := range map[string]func(input) error{
		"resources/delete": s.deleteResource, "sops/delete": s.deleteSOP, "repositories/delete": s.deleteRepository,
		"executors/recover": func(data input) error { return s.recoverExecutor(str(data, "identifier")) },
		"executors/manage": func(data input) error {
			enabled, ok := data["enabled"].(bool)
			if !ok {
				return errors.New("enabled 必须为布尔值")
			}
			return s.manage(str(data, "identifier"), enabled)
		},
	} {
		call := action
		post("/api/"+name, 200, func(_ *gin.Context, data input) (any, error) { return input{}, call(data) })
	}
	for name, action := range map[string]func(string, input) error{
		"approve": func(id string, _ input) error { return s.approve(id) },
		"retry":   func(id string, _ input) error { return s.retry(id) },
		"review": func(id string, data input) error {
			if str(data, "decision") == "returned" {
				return s.returnReview(id, str(data, "target"), str(data, "note"), str(data, "node"))
			}
			return s.review(id, str(data, "decision"), str(data, "note"), str(data, "node"))
		},
	} {
		call := action
		post("/api/requirements/:id/"+name, 200, func(c *gin.Context, data input) (any, error) {
			id := c.Param("id")
			return input{"id": id}, call(id, data)
		})
	}
	post("/api/worker/:action", 200, func(c *gin.Context, data input) (any, error) {
		if secret == "" || !matches(c.GetHeader("Authorization"), "Bearer "+secret) {
			c.JSON(401, input{"error": "执行器访问令牌无效"})
			return nil, nil
		}
		id, proof := str(data, "identifier"), str(data, "proof")
		action := c.Param("action")
		if action == "register" {
			if !proofPattern.MatchString(proof) {
				return nil, errors.New("执行器凭据格式无效")
			}
			return input{"id": id}, s.register(id, str(data, "name"), proof)
		}
		if !s.verifies(id, proof) {
			c.JSON(401, input{"error": "执行器凭据无效"})
			return nil, nil
		}
		switch action {
		case "claim":
			job, err := s.claim(id)
			return input{"job": job}, err
		case "heartbeat":
			return input{"id": id}, s.heartbeat(id)
		case "complete":
			return input{"id": id}, s.complete(id, str(data, "lease"), obj(data["artifact"]), str(data, "error"))
		default:
			c.JSON(404, input{"error": "未知执行器操作"})
			return nil, nil
		}
	})
	return r
}

func runCLI(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return errors.New("用法: ai-dev <serve|executor> [选项]")
	}
	switch args[0] {
	case "serve":
		flags := flag.NewFlagSet("serve", flag.ContinueOnError)
		root := flags.String("root", ".delivery", "SQLite 数据目录")
		host := flags.String("host", "127.0.0.1", "监听地址")
		port := flags.String("port", "8000", "监听端口")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		s, err := openStore(*root)
		if err != nil {
			return err
		}
		db, err := s.db.DB()
		if err != nil {
			return err
		}
		defer db.Close()
		address := net.JoinHostPort(*host, *port)
		server := &http.Server{Addr: address, Handler: router(s, os.Getenv("DELIVERY_EXECUTOR_TOKEN")),
			ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 30 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second}
		listener, err := net.Listen("tcp", address)
		if err != nil {
			return err
		}
		log.Printf("Management server listening on %s", listener.Addr())
		stopped := make(chan struct{})
		go func() {
			select {
			case <-ctx.Done():
				shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				_ = server.Shutdown(shutdown)
			case <-stopped:
			}
		}()
		err = server.Serve(listener)
		close(stopped)
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case "executor":
		flags := flag.NewFlagSet("executor", flag.ContinueOnError)
		root := flags.String("root", ".executor", "执行器数据目录")
		address := flags.String("url", "http://127.0.0.1:8000", "管理面 URL")
		id := flags.String("id", "local", "执行器标识")
		name := flags.String("name", "Local executor", "执行器名称")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		secret := os.Getenv("DELIVERY_EXECUTOR_TOKEN")
		os.Unsetenv("DELIVERY_EXECUTOR_TOKEN")
		if secret == "" {
			return errors.New("请设置 DELIVERY_EXECUTOR_TOKEN")
		}
		return runExecutor(ctx, *root, *address, *id, *name, secret)
	case "publish":
		if len(args) != 3 {
			return errors.New("用法: ai-dev publish <manifest> <repository>")
		}
		link, err := publish(ctx, args[1], args[2])
		if err == nil {
			fmt.Println(link)
		}
		return err
	case "-h", "--help", "help":
		fmt.Println("用法: ai-dev <serve|executor> [选项]")
		return nil
	default:
		return fmt.Errorf("未知命令: %s", args[0])
	}
}

func main() {
	gin.SetMode(gin.ReleaseMode)
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if err := runCLI(ctx, os.Args[1:]); err != nil && !errors.Is(err, flag.ErrHelp) && !errors.Is(err, context.Canceled) {
		log.Print(err)
		os.Exit(1)
	}
}
