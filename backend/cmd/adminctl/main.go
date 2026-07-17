package main

import (
	"bufio"
	"context"
	"flag"
	"log"
	"os"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	"internal-image-studio/internal/auth"
	"internal-image-studio/internal/config"
)

func main() {
	username := flag.String("username", "admin", "login username")
	displayName := flag.String("display-name", "Studio Admin", "display name")
	flag.Parse()
	*username = auth.NormalizeUsername(*username)
	*displayName = auth.NormalizeDisplayName(*displayName)
	if err := auth.ValidateUsername(*username); err != nil {
		log.Fatal(err)
	}
	if err := auth.ValidateDisplayName(*displayName); err != nil {
		log.Fatal(err)
	}
	stdinInfo, err := os.Stdin.Stat()
	if err != nil || stdinInfo.Mode()&os.ModeCharDevice != 0 {
		log.Fatal("password must be provided on stdin (for example, from a silent shell prompt); command-line passwords are not accepted")
	}
	password, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil && strings.TrimSpace(password) == "" {
		log.Fatal("read password from stdin: ", err)
	}
	password = strings.TrimSpace(password)
	hash, err := auth.HashPassword(password)
	if err != nil {
		log.Fatal(err)
	}
	ctx := context.Background()
	databaseURL, err := config.DatabaseURLFromEnv()
	if err != nil {
		log.Fatal(err)
	}
	db, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()
	_, err = db.Exec(ctx, `INSERT INTO users(username,display_name,password_hash,role,status,must_change_password) VALUES($1,$2,$3,'admin','active',false)
		ON CONFLICT(username) DO UPDATE SET display_name=excluded.display_name,password_hash=excluded.password_hash,role='admin',status='active',must_change_password=false,temporary_password_expires_at=NULL,session_version=users.session_version+1,updated_at=now()`, *username, *displayName, hash)
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("administrator %s is ready", *username)
}
