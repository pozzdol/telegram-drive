// "Log in with Telegram" through Telegram's own login popup (no widget iframe, no eval,
// so the page's CSP only has to allow the script from telegram.org).
import { api } from './api.js'

let config // { telegram_bot, telegram_bot_id } from the server; null fields = disabled
export const telegramConfig = () => (config ??= api('/api/auth/config').catch(() => ({})))

let script
const loadScript = () => (script ??= new Promise((resolve, reject) => {
  const s = document.createElement('script')
  s.src = 'https://telegram.org/js/telegram-widget.js?22'
  s.async = true
  s.onload = resolve
  s.onerror = () => { script = null; reject(new Error('Tidak bisa memuat login Telegram')) }
  document.head.appendChild(s)
}))

// Opens the popup; resolves with the signed user data, or null if the user closed it.
// request_access 'write' lets the bot message the user (comment notifications).
export async function telegramAuth(botId) {
  await loadScript()
  return new Promise(resolve => {
    window.Telegram.Login.auth({ bot_id: botId, request_access: 'write' }, data => resolve(data || null))
  })
}
