# Продление TLS-сертификатов

Сертификаты выпускает cert-manager через Let's Encrypt (ClusterIssuer letsencrypt-prod). Продление автоматическое за 30 дней до истечения.

Если сертификат не продлился: kubectl describe certificate и kubectl get challenges. Частая причина — DNS-запись не указывает на ingress, и HTTP-01 проверка не проходит.

Алерт CertExpiringSoon срабатывает за 14 дней до окончания срока.
