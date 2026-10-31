# Otwarte kwestie

Niesprawdzone albo nierozstrzygnięte. Po rozstrzygnięciu: przenieść wynik do właściwego pliku, usunąć stąd.

## Chat, Factory

- [ ] Czy skille `synced/` konta claude.ai synchronizują się same w Sandboxie przy tokenie z `setup-token` (jeśli tak, pojawią się w Chat i Factory mimo ADR-0009).
- [ ] „Pack bomb” z Sandboxa: bundle wyniku ma limit 1 GiB, ale rozmiar po rozpakowaniu w Quarantine repo (delty, duże bloby) nie jest ograniczony. Limit na obiekty (`fetch.unpackLimit`/`transfer` i kontrola rozmiaru), czy wystarczy kontrola miejsca na dysku?
