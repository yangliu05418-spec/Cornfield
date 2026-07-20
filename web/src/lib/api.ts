export type User = {
  id: string
  username: string
  display_name: string
  role: 'member' | 'admin'
  must_change_password: boolean
}

export type Model = {
  id: string
  display_name: string
  provider: string
  outputs_per_draw: number
  capabilities: {
    text_to_image: boolean
    image_to_image: boolean
    aspect_ratios: string[]
    resolutions: string[]
    midjourney_versions?: string[]
    max_reference_images: number
    max_reference_bytes: number
    draw_count: { min: number; max: number; default: number }
  }
}

export type Asset = {
  id: string
  kind: 'upload' | 'generation'
  media_type: string
  original_filename?: string
  width: number
  height: number
  byte_size: number
  sha256: string
  blur_data_url?: string
  batch_id?: string
  job_id?: string
  output_index?: number
  url: string
  thumb_320_url: string
  thumb_640_url: string
  thumb_1280_url: string
  created_at: string
}

export type AssetPage = {
  items: Asset[]
  next_cursor: string
}

export type GenerationJob = {
  id: string
  draw_index: number
  status: string
  expected_outputs: number
  error_code?: string
  error_message?: string
  outputs?: GenerationOutput[]
  deleted_outputs?: number[]
}

export type MidjourneyOptions = {
  version: '8.1' | '7'
  resolution?: 'sd' | 'hd'
  speed: 'fast' | 'turbo'
  quality?: 1 | 2 | 4
  draft: boolean
  stylize: number
  chaos: number
  weird: number
  raw: boolean
  tile: boolean
  image_weight?: number
}

export type GenerationOptions = {
  midjourney?: MidjourneyOptions
}

export type GenerationOutput = {
  asset_id: string
  output_index: number
  width: number
  height: number
  media_type: string
  url: string
  thumb_320_url: string
  thumb_640_url: string
  thumb_1280_url: string
}

export type GenerationBatch = {
  id: string
  model_id: string
  prompt: string
  aspect_ratio: string
  resolution: string
  draw_count: number
  expected_outputs: number
  completed_outputs: number
  status: string
  created_at: string
  jobs: GenerationJob[]
  options?: GenerationOptions
}

export class APIError extends Error {
  code: string
  status: number
  constructor(status: number, code: string, message: string) {
    super(message)
    this.status = status
    this.code = code
  }
}

export const authExpiredEvent = 'cornfield:auth-expired'

function cookie(name: string) {
  if (typeof document === 'undefined') return ''
  return (
    document.cookie
      .split('; ')
      .find((item) => item.startsWith(`${name}=`))
      ?.split('=')
      .slice(1)
      .join('=') ?? ''
  )
}

export async function api<T>(path: string, init: RequestInit = {}): Promise<T> {
  const headers = new Headers(init.headers)
  if (
    init.body &&
    !(init.body instanceof Blob) &&
    !(init.body instanceof FormData)
  )
    headers.set('Content-Type', 'application/json')
  if (init.method && !['GET', 'HEAD'].includes(init.method))
    headers.set('X-CSRF-Token', decodeURIComponent(cookie('studio_csrf')))
  const response = await fetch(path, {
    ...init,
    headers,
    credentials: 'same-origin',
  })
  if (
    response.status === 401 &&
    path !== '/api/v1/auth/login' &&
    typeof window !== 'undefined'
  ) {
    window.dispatchEvent(new Event(authExpiredEvent))
  }
  if (!response.ok) {
    const body = (await response.json().catch(() => null)) as {
      error?: { code?: string; message?: string }
    } | null
    throw new APIError(
      response.status,
      body?.error?.code ?? 'REQUEST_FAILED',
      body?.error?.message ?? '请求失败',
    )
  }
  if (response.status === 204) return undefined as T
  return response.json() as Promise<T>
}

export const getMe = () => api<{ user: User }>('/api/v1/auth/me')
