import * as token from './providers/token.js'
import { save } from './session.js'

const providers = { token } // 接 IDP 時加一行：oidc
const providerName = import.meta.env.VITE_AUTH_PROVIDER ?? 'token'

export async function login(input) {
  save(await providers[providerName].login(input))
}
