package auth

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

// DemoPassword tüm demo hesapların parolasıdır (yalnızca geliştirme/değerlendirme içindir).
const DemoPassword = "Demo1234!"

type demoUser struct{ Email, Name, Role string }

var demoUsers = []demoUser{
	{"ogrenci@uninet.test", "Ayşe Öğrenci", RoleStudent},
	{"hoca@uninet.test", "Mehmet Öğretim Üyesi", RoleInstructor},
	{"danisman@uninet.test", "Zeynep Danışman", RoleAdvisor},
	{"sekreter@uninet.test", "Ali Bölüm Sekreteri", RoleDeptSecretary},
	{"ogrenciisleri@uninet.test", "Fatma Öğrenci İşleri", RoleRegistrar},
	{"rektorluk@uninet.test", "Hasan Raporlama", RoleReporting},
}

// SeedDemoUsers her rol için bir hesap oluşturur; idempotenttir (varsa dokunmaz).
func SeedDemoUsers(ctx context.Context, pool *pgxpool.Pool) error {
	for _, u := range demoUsers {
		hash, err := HashPassword(DemoPassword)
		if err != nil {
			return err
		}
		if _, err := pool.Exec(ctx,
			`INSERT INTO users (email, password_hash, full_name, role) VALUES ($1, $2, $3, $4)
			 ON CONFLICT (email) DO NOTHING`, u.Email, hash, u.Name, u.Role); err != nil {
			return fmt.Errorf("seed %s: %w", u.Email, err)
		}
	}
	return nil
}
