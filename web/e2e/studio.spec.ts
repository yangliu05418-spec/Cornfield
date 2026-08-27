import { readFileSync } from 'node:fs'
import { expect, test } from '@playwright/test'
import type { Page } from '@playwright/test'
import type { EditorDocumentV3 } from '../src/features/editor/domain/document-v3'

const mockImage = `<svg xmlns="http://www.w3.org/2000/svg" width="1200" height="900" viewBox="0 0 1200 900"><defs><linearGradient id="g" x2="1" y2="1"><stop stop-color="#15191d"/><stop offset=".52" stop-color="#556b3a"/><stop offset="1" stop-color="#d1fe17"/></linearGradient></defs><rect width="1200" height="900" fill="url(#g)"/><circle cx="770" cy="300" r="180" fill="#d1fe17" opacity=".28"/><path d="M0 690 Q330 510 660 700 T1200 620 V900 H0Z" fill="#090b0c" opacity=".76"/></svg>`

test('landing page uses the production static shell', async ({
  page,
}, testInfo) => {
  const errors = capturePageErrors(page)
  await page.setViewportSize({ width: 1440, height: 960 })
  await page.goto('/')

  await expect(page).toHaveTitle('Cornfield — 未来影像工作室')
  await expect(page.getByRole('heading', { level: 1 })).toContainText(
    '让想象先于现实。',
  )
  await expect(page.locator('.landing-cube-mark').first()).toHaveAttribute(
    'src',
    '/cornfield-cube.svg',
  )
  await expect(page.locator('.landing-orbit-item')).toHaveCount(30)
  await expect(page.locator('.landing-orbit-item').first()).toHaveCSS(
    'animation-name',
    'landing-orbit',
  )
  const filmImage = await page.locator('.landing-film-image').boundingBox()
  const filmMark = await page
    .locator('.landing-film-frame .landing-cube-mark')
    .boundingBox()
  expect(filmImage?.width).toBeGreaterThan(1_000)
  expect(filmMark?.width).toBeLessThanOrEqual(24)
  expect(filmMark?.height).toBeLessThanOrEqual(24)
  await expect(
    page.getByRole('link', { name: '进入工作室' }).first(),
  ).toHaveAttribute('href', '/app/login')
  const executableInlineScripts = await page
    .locator('script:not([src])')
    .evaluateAll(
      (scripts) => scripts.filter((script) => script.textContent.trim()).length,
    )
  expect(executableInlineScripts).toBe(0)
  await page.screenshot({
    path: testInfo.outputPath('landing-desktop.png'),
    fullPage: true,
  })
  await page.setViewportSize({ width: 390, height: 844 })
  for (const sentence of await page
    .locator('.landing-hero h1 span, .landing-closing h2 span')
    .all()) {
    expect(
      await sentence.evaluate(
        (element) => element.scrollWidth <= element.clientWidth,
      ),
    ).toBe(true)
  }
  await page.screenshot({ path: testInfo.outputPath('landing.png') })
  expect(errors).toEqual([])
})

test('desktop studio supports density, preview, and optimistic generation', async ({
  page,
}, testInfo) => {
  const errors = capturePageErrors(page)
  await page.setViewportSize({ width: 1440, height: 960 })
  await installStudioMocks(page)
  await page.goto('/app/create')

  const density = page.getByRole('slider', { name: '调整图片墙缩放' })
  await expect(density).toHaveValue('2')
  await page.getByRole('button', { name: '放大图片' }).click()
  await expect(density).toHaveValue('3')

  const firstCard = page
    .getByRole('article', { name: '打开生成图片预览' })
    .first()
  await firstCard.focus()
  await firstCard.press('Enter')
  await expect(page.getByRole('dialog', { name: '图片预览' })).toBeVisible()
  await page.keyboard.press('Escape')
  await expect(page.getByRole('dialog', { name: '图片预览' })).toBeHidden()

  await page
    .getByRole('textbox', { name: '生成提示词' })
    .fill('A quiet cornfield under a distant ringed planet')
  await page.getByRole('button', { name: '增加抽卡' }).click()
  await page.getByRole('button', { name: '生成', exact: true }).click()

  await expect(page.getByText('正在创建', { exact: true })).toHaveCount(2, {
    timeout: 700,
  })
  await expect(page.locator('.generate-button')).toHaveText('提交中…')
  await expect(page.getByText('排队中', { exact: true })).toHaveCount(2, {
    timeout: 5_000,
  })
  const cancelResponse = page.waitForResponse(
    (response) =>
      response.url().endsWith('/cancel') &&
      response.request().method() === 'POST',
  )
  await page.getByRole('button', { name: '取消这次抽卡' }).first().click()
  expect((await cancelResponse).status()).toBe(202)
  await expect(
    page.getByText('已停止等待并会丢弃迟到结果；上游可能已经产生费用'),
  ).toBeVisible()
  await page.screenshot({ path: testInfo.outputPath('desktop-studio.png') })
  expect(errors).toEqual([])
})

test('prompt refiner replaces in place, shows its change, and remains undoable', async ({
  page,
}) => {
  const studio = await installStudioMocks(page, { feedbackFails: true })
  await page.goto('/app/create')
  const prompt = page.getByRole('textbox', { name: '生成提示词' })
  await prompt.fill('blood over a quiet cornfield')
  expect(studio.refineAttempts()).toBe(0)

  await page.getByRole('button', { name: '检查并优化提示词' }).click()
  await expect(prompt).toHaveValue('crimson liquid over a quiet cornfield')
  const refinerNotice = page.locator('.prompt-refiner-undo')
  await expect(refinerNotice).toBeVisible()
  await expect(refinerNotice).toHaveCSS('position', 'absolute')
  await page.waitForTimeout(200)
  const noticeBox = await refinerNotice.boundingBox()
  const generatorBox = await page.locator('.generator').boundingBox()
  expect(noticeBox).not.toBeNull()
  expect(generatorBox).not.toBeNull()
  expect(noticeBox!.y + noticeBox!.height).toBeLessThan(generatorBox!.y)
  expect(noticeBox!.x).toBeGreaterThanOrEqual(generatorBox!.x)
  expect(noticeBox!.x + noticeBox!.width).toBeLessThanOrEqual(
    generatorBox!.x + generatorBox!.width,
  )
  await page.getByRole('button', { name: '查看修改' }).click()
  const review = page.getByRole('dialog', { name: '查看提示词修改' })
  await expect(review.getByLabel('修改前')).toContainText(
    'blood over a quiet cornfield',
  )
  await expect(review.getByLabel('修改后')).toContainText(
    'crimson liquid over a quiet cornfield',
  )
  await review.getByRole('button', { name: '关闭修改对比' }).click()
  await page.getByRole('button', { name: '撤销' }).click()
  await expect(prompt).toHaveValue('blood over a quiet cornfield')
  await expect
    .poll(() => studio.refinementFeedback())
    .toEqual([
      { refinementID: 'refinement-1', event: 'undone', batch_id: undefined },
    ])
  await prompt.fill('clean-check')
  await prompt.press('Tab')
  await page.getByRole('button', { name: '检查并优化提示词' }).click()
  await expect(page.getByText('提示词已检查，无需修改')).toBeVisible()
  await expect(prompt).toHaveValue('clean-check')
  expect(studio.refineAttempts()).toBe(2)
})

test('prompt refiner keeps the original prompt when its response is invalid', async ({
  page,
}) => {
  const studio = await installStudioMocks(page, {
    refinerFailure: {
      status: 502,
      code: 'PROMPT_REFINER_INVALID_RESPONSE',
      message: '优化结果格式异常，原提示词未被修改',
    },
  })
  await page.goto('/app/create')
  const prompt = page.getByRole('textbox', { name: '生成提示词' })
  await prompt.fill('blood over a quiet cornfield')

  await page.getByRole('button', { name: '检查并优化提示词' }).click()

  await expect(
    page.getByText('优化结果格式异常，原提示词未被修改'),
  ).toBeVisible()
  await expect(prompt).toHaveValue('blood over a quiet cornfield')
  await expect(
    page.getByRole('button', { name: '检查并优化提示词' }),
  ).toBeEnabled()
  await expect(page.getByRole('button', { name: '查看修改' })).toHaveCount(0)
  await expect(page.getByRole('button', { name: '撤销' })).toHaveCount(0)
  expect(studio.refineAttempts()).toBe(1)
  expect(studio.postAttempts()).toBe(0)
})

test('prompt refiner never overwrites text edited while its request is running', async ({
  page,
}) => {
  // Keep the mocked request open long enough for this assertion even when
  // the full Playwright suite is sharing CPU across workers.
  await installStudioMocks(page, { refinerDelayMs: 1_000 })
  await page.goto('/app/create')
  const prompt = page.getByRole('textbox', { name: '生成提示词' })
  await prompt.fill('blood over a quiet cornfield')
  await page.getByRole('button', { name: '检查并优化提示词' }).click()
  await expect(
    page.getByRole('button', { name: '正在优化提示词' }),
  ).toBeVisible()
  await expect(
    page.getByRole('button', { name: '生成', exact: true }),
  ).toBeDisabled()

  await prompt.fill('A new idea typed while optimization is running')
  await page.waitForTimeout(250)

  await expect(prompt).toHaveValue(
    'A new idea typed while optimization is running',
  )
  await expect(page.getByText('提示词已优化')).toHaveCount(0)
})

test('an unchanged optimized prompt reports the successful generation', async ({
  page,
}) => {
  const studio = await installStudioMocks(page)
  await page.goto('/app/create')
  const prompt = page.getByRole('textbox', { name: '生成提示词' })
  await prompt.fill('blood over a quiet cornfield')
  await page.getByRole('button', { name: '检查并优化提示词' }).click()
  await expect(prompt).toHaveValue('crimson liquid over a quiet cornfield')

  await page.getByRole('button', { name: '生成', exact: true }).click()
  await expect(page.getByText('排队中', { exact: true })).toBeVisible({
    timeout: 5_000,
  })
  await expect
    .poll(() => studio.refinementFeedback())
    .toEqual([
      {
        refinementID: 'refinement-1',
        event: 'submitted',
        batch_id: 'batch-qa',
      },
    ])
  await expect(page.getByRole('button', { name: '撤销' })).toHaveCount(0)
})

test('a text-fixable failed card restores context and refines without generating', async ({
  page,
}) => {
  const failedBatch = {
    id: 'policy-batch',
    model_id: 'nano-banana-pro',
    prompt: 'blood over a quiet cornfield',
    aspect_ratio: '1:1',
    resolution: '1K',
    draw_count: 1,
    expected_outputs: 1,
    completed_outputs: 0,
    status: 'failed',
    created_at: new Date().toISOString(),
    options: {},
    input_asset_ids: [],
    jobs: [
      {
        id: 'policy-job',
        draw_index: 0,
        status: 'failed',
        expected_outputs: 1,
        error_code: 'CONTENT_POLICY_REJECTED',
        retryable: false,
      },
    ],
  }
  const studio = await installStudioMocks(page, {
    generationPages: {
      '': { items: [failedBatch], next_cursor: '' },
    },
  })
  await page.goto('/app/create')

  await page.getByRole('button', { name: '优化提示词', exact: true }).click()

  await expect(page.getByRole('textbox', { name: '生成提示词' })).toHaveValue(
    'crimson liquid over a quiet cornfield',
  )
  expect(studio.postAttempts()).toBe(0)
})

test('prompt grows to a bounded height and accepts mixed clipboard content', async ({
  page,
}) => {
  await installStudioMocks(page)
  await page.goto('/app/create')
  const prompt = page.getByRole('textbox', { name: '生成提示词' })

  const initialHeight = await prompt.evaluate((element) => element.clientHeight)
  await prompt.fill(
    Array.from({ length: 12 }, (_, index) => `第${index + 1}行`).join('\n'),
  )
  const grown = await prompt.evaluate((element) => ({
    height: element.clientHeight,
    scrollHeight: element.scrollHeight,
    overflowY: getComputedStyle(element).overflowY,
  }))
  expect(grown.height).toBeGreaterThan(initialHeight)
  expect(grown.height).toBeLessThanOrEqual(136)
  expect(grown.scrollHeight).toBeGreaterThan(grown.height)
  expect(grown.overflowY).toBe('auto')

  await prompt.fill('镜头：')
  await prompt.evaluate((element) => {
    const data = new DataTransfer()
    data.setData('text/plain', '雨中的街道')
    data.items.add(
      new File([new Uint8Array([0xff, 0xd8, 0xff, 0xd9])], 'reference.jpg', {
        type: 'image/jpeg',
      }),
    )
    element.dispatchEvent(
      new ClipboardEvent('paste', {
        bubbles: true,
        cancelable: true,
        clipboardData: data,
      }),
    )
  })

  await expect(prompt).toHaveValue('镜头：雨中的街道')
  await expect(page.getByRole('img', { name: '参考图' })).toHaveCount(1)
})

test('prompt drop zone previews a dragged reference image', async ({
  page,
}, testInfo) => {
  await installStudioMocks(page)
  await page.goto('/app/create')
  const dropZone = page.locator('.generator-prompt-row')

  await dropZone.evaluate((element) => {
    const data = new DataTransfer()
    data.items.add(
      new File([new Uint8Array([0xff, 0xd8, 0xff, 0xd9])], 'dragged.jpg', {
        type: 'image/jpeg',
      }),
    )
    ;(
      window as typeof window & { __referenceDrag?: DataTransfer }
    ).__referenceDrag = data
    element.dispatchEvent(
      new DragEvent('dragenter', {
        bubbles: true,
        cancelable: true,
        dataTransfer: data,
      }),
    )
  })
  await expect(
    page.getByRole('status').filter({ hasText: '松开，将图片置入参考区' }),
  ).toBeVisible()
  await page.screenshot({ path: testInfo.outputPath('prompt-drop-zone.png') })

  await dropZone.evaluate((element) => {
    const scopedWindow = window as typeof window & {
      __referenceDrag?: DataTransfer
    }
    element.dispatchEvent(
      new DragEvent('drop', {
        bubbles: true,
        cancelable: true,
        dataTransfer: scopedWindow.__referenceDrag,
      }),
    )
    delete scopedWindow.__referenceDrag
  })

  await expect(page.getByText('松开，将图片置入参考区')).toBeHidden()
  await expect(page.getByRole('img', { name: '参考图' })).toHaveCount(1)
})

test('local reference previews immediately and uploads only when generating', async ({
  page,
}) => {
  const studio = await installStudioMocks(page)
  await page.goto('/app/create')
  await expect(page.getByRole('article')).toHaveCount(18)
  const initialWallCount = await page.getByRole('article').count()

  await page
    .locator('input[type="file"][aria-label="添加参考图"]')
    .setInputFiles({
      name: 'local-reference.jpg',
      mimeType: 'image/jpeg',
      buffer: Buffer.from([0xff, 0xd8, 0xff, 0xd9]),
    })

  await expect(page.getByRole('img', { name: '参考图' })).toHaveCount(1)
  expect(studio.uploadAttempts()).toBe(0)
  await page.getByRole('textbox', { name: '生成提示词' }).fill('雨夜中的车站')
  await page.getByRole('button', { name: '生成', exact: true }).click()

  await expect.poll(studio.uploadAttempts).toBe(1)
  await expect
    .poll(() => studio.lastGenerationInput()?.input_asset_ids)
    .toEqual(['asset-editor-upload'])
  expect(studio.lastUploadPurpose()).toBe('reference')
  expect(await page.getByRole('article').count()).toBe(initialWallCount + 1)
})

test('reference previews stay legible and keep remove controls inside each card', async ({
  page,
}, testInfo) => {
  await page.setViewportSize({ width: 1440, height: 960 })
  const studio = await installStudioMocks(page)
  await page.goto('/app/create')
  await page
    .locator('input[type="file"][aria-label="添加参考图"]')
    .evaluate(async (element) => {
      const data = new DataTransfer()
      const sizes = [
        [1600, 900],
        [800, 1200],
        [1000, 1000],
      ]
      for (const [index, [width, height]] of sizes.entries()) {
        const canvas = document.createElement('canvas')
        canvas.width = width
        canvas.height = height
        const context = canvas.getContext('2d')
        if (!context) throw new Error('canvas is unavailable')
        context.fillStyle = ['#26344a', '#46532b', '#533742'][index]
        context.fillRect(0, 0, width, height)
        const blob = await new Promise<Blob>((resolve, reject) =>
          canvas.toBlob(
            (value) =>
              value ? resolve(value) : reject(new Error('PNG encode failed')),
            'image/png',
          ),
        )
        data.items.add(
          new File([blob], `reference-${index + 1}.png`, {
            type: 'image/png',
          }),
        )
      }
      ;(element as HTMLInputElement).files = data.files
      element.dispatchEvent(new Event('change', { bubbles: true }))
    })

  const cards = page.locator('.reference-card')
  const removeButtons = page.getByRole('button', { name: '移除参考图' })
  await expect(cards).toHaveCount(3)
  await expect(removeButtons).toHaveCount(3)
  await expect(cards.first().locator('img')).toHaveCSS('object-fit', 'contain')
  await expect(cards.first()).toHaveCSS('height', '96px')
  await expect(cards.first()).toHaveAttribute(
    'style',
    /aspect-ratio: 1600 \/ 900/,
  )
  const desktopWidths = await cards.evaluateAll((items) =>
    items.map((item) => item.getBoundingClientRect().width),
  )
  expect(desktopWidths[0]).toBeGreaterThan(desktopWidths[2])
  expect(desktopWidths[2]).toBeGreaterThan(desktopWidths[1])
  await page.screenshot({
    path: testInfo.outputPath('reference-previews-desktop.png'),
  })

  await page.setViewportSize({ width: 390, height: 844 })
  await expect(cards.first()).toHaveCSS('height', '80px')

  for (let index = 0; index < 3; index += 1) {
    const cardBox = await cards.nth(index).boundingBox()
    const buttonBox = await removeButtons.nth(index).boundingBox()
    expect(cardBox).not.toBeNull()
    expect(buttonBox).not.toBeNull()
    expect(buttonBox!.x).toBeGreaterThanOrEqual(cardBox!.x)
    expect(buttonBox!.y).toBeGreaterThanOrEqual(cardBox!.y)
    expect(buttonBox!.x + buttonBox!.width).toBeLessThanOrEqual(
      cardBox!.x + cardBox!.width,
    )
    expect(buttonBox!.y + buttonBox!.height).toBeLessThanOrEqual(
      cardBox!.y + cardBox!.height,
    )
  }

  await page.screenshot({
    path: testInfo.outputPath('reference-previews-mobile.png'),
  })
  await removeButtons.nth(1).click()
  await expect(cards).toHaveCount(2)
  expect(studio.uploadAttempts()).toBe(0)
})

test('a restored temporary-password session cannot enter the studio', async ({
  page,
}) => {
  await installStudioMocks(page, {
    user: {
      id: 'temporary-user',
      username: 'temporary',
      display_name: 'Temporary',
      role: 'member',
      must_change_password: true,
    },
  })
  await page.goto('/app/create')
  await expect(page).toHaveURL(/\/app\/change-password$/)
  await expect(page.getByRole('heading', { name: '修改密码' })).toBeVisible()
})

test('account menu exposes password change', async ({ page }) => {
  await installStudioMocks(page)
  await page.goto('/app/create')

  await page.getByRole('button', { name: '打开账户菜单' }).click()
  await page.getByRole('link', { name: '修改密码' }).click()

  await expect(page).toHaveURL(/\/app\/change-password$/)
  await expect(page.getByRole('heading', { name: '修改密码' })).toBeVisible()
  await expect(page.getByRole('button', { name: '退出登录' })).toBeVisible()
  const panel = page.locator('.login-panel')
  const backLink = panel.getByRole('link', { name: '返回工作区' })
  await expect(backLink).toBeVisible()
  await backLink.click()
  await expect(page).toHaveURL(/\/app\/create$/)
})

test('a protected API 401 clears the studio and returns to login', async ({
  page,
}) => {
  const studio = await installStudioMocks(page)
  await page.goto('/app/create')
  await expect(
    page.getByRole('article', { name: '打开生成图片预览' }).first(),
  ).toBeVisible()

  studio.revoke()
  await page.getByRole('link', { name: '资产' }).click()

  await expect(page).toHaveURL(/\/app\/login$/)
  await expect(
    page.getByRole('heading', { name: '回到创作现场' }),
  ).toBeVisible()
  await expect(
    page.getByRole('article', { name: '打开生成图片预览' }),
  ).toHaveCount(0)
})

test('loads the next generation page so an older active draw stays cancellable', async ({
  page,
}) => {
  await installStudioMocks(page, {
    generationPages: {
      '': {
        items: [
          {
            id: 'finished-batch',
            model_id: 'nano-banana-pro',
            prompt: 'finished',
            aspect_ratio: '1:1',
            resolution: '1K',
            draw_count: 1,
            expected_outputs: 1,
            completed_outputs: 1,
            status: 'succeeded',
            created_at: new Date().toISOString(),
            jobs: [
              {
                id: 'finished-job',
                draw_index: 0,
                status: 'succeeded',
                expected_outputs: 1,
                outputs: [],
              },
            ],
          },
        ],
        next_cursor: 'older',
      },
      older: {
        items: [
          {
            id: 'active-batch',
            model_id: 'nano-banana-pro',
            prompt: 'older active draw',
            aspect_ratio: '1:1',
            resolution: '1K',
            draw_count: 1,
            expected_outputs: 1,
            completed_outputs: 0,
            status: 'queued',
            created_at: new Date(Date.now() - 1_000).toISOString(),
            jobs: [
              {
                id: 'active-job',
                draw_index: 0,
                status: 'queued',
                expected_outputs: 1,
                outputs: [],
              },
            ],
          },
        ],
        next_cursor: '',
      },
    },
  })
  await page.goto('/app/create')

  await expect(page.getByText('older active draw')).toBeVisible()
  await expect(page.getByRole('button', { name: '取消这次抽卡' })).toBeVisible()
})

test('retries a lost create response with the same idempotency key', async ({
  page,
}) => {
  const studio = await installStudioMocks(page, {
    generationPostNetworkFailures: 2,
  })
  await page.goto('/app/create')
  await page
    .getByRole('textbox', { name: '生成提示词' })
    .fill('One request across a broken connection')
  await page.getByRole('button', { name: '生成', exact: true }).click()

  await expect(page.getByText('排队中', { exact: true })).toBeVisible({
    timeout: 7_000,
  })
  expect(studio.postAttempts()).toBe(3)
  expect(new Set(studio.postKeys()).size).toBe(1)
  expect(studio.postKeys()[0]).not.toBe('')
})

test('polling restores a completed asset when no SSE job event arrives', async ({
  page,
}) => {
  const studio = await installStudioMocks(page)
  await page.goto('/app/create')
  await expect(page.locator('img[src*="asset=0"]')).toBeVisible()

  studio.prependAsset({
    id: 'fallback',
    kind: 'generation',
    media_type: 'image/webp',
    width: 1024,
    height: 1024,
    byte_size: 1_000,
    sha256: 'hash-fallback',
    url: '/mock-image.svg?asset=fallback',
    thumb_320_url: '/mock-image.svg?asset=fallback&size=320',
    thumb_640_url: '/mock-image.svg?asset=fallback&size=640',
    thumb_1280_url: '/mock-image.svg?asset=fallback&size=1280',
    created_at: new Date().toISOString(),
  })
  studio.setGenerations([
    {
      id: 'fallback-batch',
      model_id: 'nano-banana-pro',
      prompt: 'completed while SSE was unavailable',
      aspect_ratio: '1:1',
      resolution: '1K',
      draw_count: 1,
      expected_outputs: 1,
      completed_outputs: 1,
      status: 'succeeded',
      created_at: new Date().toISOString(),
      jobs: [
        {
          id: 'fallback-job',
          draw_index: 0,
          status: 'succeeded',
          expected_outputs: 1,
          outputs: [],
        },
      ],
    },
  ])

  await expect(page.locator('img[src*="asset=fallback"]')).toBeVisible({
    timeout: 13_000,
  })
})

test('Midjourney stays one draw with four outputs and versioned parameters', async ({
  page,
}) => {
  await installStudioMocks(page, {
    models: [
      {
        id: 'legnext-midjourney',
        display_name: 'Midjourney',
        provider: 'legnext',
        outputs_per_draw: 4,
        availability: { state: 'healthy', can_submit: true },
        capabilities: {
          text_to_image: true,
          image_to_image: true,
          aspect_ratios: ['1:1', '16:9'],
          resolutions: ['SD', 'HD'],
          midjourney_versions: ['8.2', '8.1', '8', '7', '6.1', '6', 'niji 6'],
          max_reference_images: 4,
          max_reference_bytes: 10_485_760,
          draw_count: { min: 1, max: 1, default: 1 },
        },
      },
      {
        id: 'gpt-image-2',
        display_name: 'GPT Image 2',
        provider: 'openrouter',
        outputs_per_draw: 1,
        availability: { state: 'healthy', can_submit: true },
        capabilities: {
          text_to_image: true,
          image_to_image: true,
          aspect_ratios: [],
          resolutions: [],
          max_reference_images: 4,
          max_reference_bytes: 10_485_760,
          draw_count: { min: 1, max: 4, default: 1 },
        },
      },
    ],
  })
  await page.goto('/app/create')

  await expect(page.getByText('4 张/次')).toBeVisible()
  await expect(page.getByRole('button', { name: '增加抽卡' })).toHaveCount(0)
  await page.getByRole('button', { name: 'V8.2 · SD' }).click()
  await expect(page.getByText('Midjourney 参数')).toBeVisible()
  await expect(
    page.getByRole('button', { name: 'V8', exact: true }),
  ).toBeVisible()
  await expect(
    page.getByRole('button', { name: 'V6.1', exact: true }),
  ).toBeVisible()
  await expect(
    page.getByRole('button', { name: 'Niji 6', exact: true }),
  ).toBeVisible()
  await page.keyboard.press('Escape')

  await page.getByRole('textbox', { name: '生成提示词' }).fill('one draw')
  const requestPromise = page.waitForRequest(
    (request) =>
      request.url().endsWith('/api/v1/generations') &&
      request.method() === 'POST',
  )
  await page.getByRole('button', { name: '生成', exact: true }).click()
  const body = (await requestPromise).postDataJSON() as {
    draw_count: number
    options: { midjourney: { version: string; resolution: string } }
  }
  expect(body.draw_count).toBe(1)
  expect(body.options.midjourney).toMatchObject({
    version: '8.2',
    resolution: 'sd',
  })

  await page.getByRole('combobox', { name: '选择模型' }).click()
  await page.getByRole('option', { name: 'GPT Image 2' }).click()
  await expect(
    page.getByRole('combobox', { name: '选择画面比例' }),
  ).toHaveCount(0)
  await expect(page.getByRole('combobox', { name: '选择分辨率' })).toHaveCount(
    0,
  )
})

test('paused provider keeps parameters visible but disables generation', async ({
  page,
}) => {
  await installStudioMocks(page, {
    models: [
      {
        id: 'nano-banana-pro',
        display_name: 'Nano Banana Pro',
        provider: 'openrouter',
        outputs_per_draw: 1,
        availability: {
          state: 'paused',
          can_submit: false,
          message: '生成服务暂不可用，请稍后重试',
        },
        capabilities: {
          text_to_image: true,
          image_to_image: true,
          aspect_ratios: ['1:1'],
          resolutions: ['1K'],
          max_reference_images: 4,
          max_reference_bytes: 26_214_400,
          draw_count: { min: 1, max: 4, default: 1 },
        },
      },
    ],
  })
  await page.goto('/app/create')

  await expect(page.locator('.generator-unavailable')).toHaveText(
    '生成服务暂不可用，请稍后重试',
  )
  await expect(page.locator('.generate-button')).toBeDisabled()
  await expect(page.getByRole('combobox', { name: '选择模型' })).toContainText(
    'Nano Banana Pro（暂不可用）',
  )
})

test('director iframe handshakes once and protects a damaged project', async ({
  page,
}) => {
  const projectID = '5d4427a8-57e4-4f37-bf15-caf6d2fc5e64'
  await installStudioMocks(page)
  let revision = 3
  await page.route(
    `**/api/v1/director-projects/${projectID}`,
    async (route) => {
      if (route.request().method() !== 'GET') return route.fallback()
      return json(route, {
        id: projectID,
        name: '损坏工程',
        revision,
        document: {
          format: '3d-director-desk-project',
          schemaVersion: 1,
          project: {},
        },
        created_at: '2026-08-01T00:00:00Z',
        updated_at: '2026-08-01T00:00:00Z',
      })
    },
  )
  await page.route(
    `**/api/v1/director-projects/${projectID}/reset`,
    async (route) => {
      revision += 1
      return json(route, { revision })
    },
  )
  await page.route('**/director-desk/**', (route) =>
    route.fulfill({
      contentType: 'text/html',
      body: `<!doctype html><body data-sessions="0"><script>
        window.addEventListener('message', (event) => {
          if (event.origin !== location.origin || event.data?.type !== 'storyai:director-desk-session') return;
          document.body.dataset.sessions = String(Number(document.body.dataset.sessions) + 1);
          if (event.data.payload?.projectDocument) {
            parent.postMessage({type:'storyai:director-desk-session-error',payload:{code:'invalid-project-document',message:'工程内容不完整或已经损坏'}}, location.origin);
          }
        });
        parent.postMessage({type:'storyai:director-desk-ready'}, location.origin);
      </script></body>`,
    }),
  )

  await page.goto(`/app/director/${projectID}`)
  const frameBody = page.frameLocator('iframe').locator('body')
  await expect(frameBody).toHaveAttribute('data-sessions', '1')
  await expect(
    page.getByRole('heading', { name: '工程内容无法读取' }),
  ).toBeVisible()

  const download = page.waitForEvent('download')
  await page.getByRole('button', { name: '下载原始 JSON' }).click()
  expect((await download).suggestedFilename()).toBe('损坏工程.json')

  await page.getByRole('button', { name: '重置为空工程' }).click()
  await page.getByRole('button', { name: '确认重置' }).click()
  await expect(
    page.getByRole('heading', { name: '工程内容无法读取' }),
  ).toBeHidden()
  await expect(frameBody).toHaveAttribute('data-sessions', '2')
})

test('asset workspace creates folders, moves assets, and archives without deleting', async ({
  page,
}, testInfo) => {
  await installStudioMocks(page)
  await page.goto('/app/assets')

  await expect(page.getByRole('heading', { name: '资产工作台' })).toBeVisible()
  await expect(page.getByPlaceholder('搜索文件名或描述')).toBeVisible()
  await page.getByRole('button', { name: '永久删除' }).first().click()
  const confirm = page.getByRole('dialog', { name: '永久删除资产' })
  await expect(confirm).toBeVisible()
  const dialogBox = await confirm.boundingBox()
  const viewport = page.viewportSize()
  expect(dialogBox).not.toBeNull()
  expect(viewport).not.toBeNull()
  expect(
    Math.abs(dialogBox!.x + dialogBox!.width / 2 - viewport!.width / 2),
  ).toBeLessThan(2)
  expect(
    Math.abs(dialogBox!.y + dialogBox!.height / 2 - viewport!.height / 2),
  ).toBeLessThan(2)
  const cancelBox = await confirm
    .getByRole('button', { name: '取消' })
    .boundingBox()
  const deleteBox = await confirm
    .getByRole('button', { name: '确认删除' })
    .boundingBox()
  expect(cancelBox?.height).toBe(deleteBox?.height)
  expect(cancelBox?.width).toBeGreaterThan(50)
  await page.keyboard.press('Escape')
  await page.getByRole('button', { name: '新建文件夹' }).click()
  await page.getByLabel('名称').fill('Campaign A')
  await page.getByRole('button', { name: '保存', exact: true }).click()
  await expect(page.getByRole('button', { name: /Campaign A/ })).toBeVisible()

  const move = page.getByRole('combobox', { name: '移动到文件夹' }).first()
  await move.selectOption({ label: 'Campaign A' })
  await expect(move).toHaveValue('folder-1')
  await page.screenshot({ path: testInfo.outputPath('asset-workspace.png') })

  await page.getByRole('button', { name: '归档', exact: true }).first().click()
  await page.getByRole('button', { name: '已归档', exact: true }).click()
  await expect(
    page.getByRole('button', { name: '取消归档' }).first(),
  ).toBeVisible()
})

test.skip('legacy V1 editor restores a source project and autosaves keyboard edits', async ({
  page,
}) => {
  const backend = await installStudioMocks(page)
  await page.goto('/app/create')

  const card = page.getByRole('article', { name: '打开生成图片预览' }).first()
  await card.hover()
  await card.getByRole('button', { name: '编辑图片' }).click()
  await expect(page).toHaveURL(/\/app\/editor\/editor-project-1$/)
  await expect(page.locator('.app-nav')).toHaveCount(0)

  await expect(page.locator('.editor-artboard-label')).toContainText(
    '1024 × 1024',
  )
  const surfaces = await page.evaluate(() => ({
    workspace: getComputedStyle(
      document.querySelector('.editor-canvas-viewport')!,
    ).backgroundColor,
    artboard: getComputedStyle(document.querySelector('.editor-artboard')!)
      .backgroundColor,
  }))
  expect(surfaces.workspace).not.toBe(surfaces.artboard)

  const artboardSettings = page.locator('.editor-artboard-settings')
  await artboardSettings.locator('input').nth(0).fill('1200')
  await artboardSettings.locator('input').nth(1).fill('900')
  await artboardSettings.locator('button').click()
  await expect.poll(() => backend.editorState().revision).toBe(1)
  expect(backend.editorState().document.canvas).toEqual({
    width: 1200,
    height: 900,
  })

  const canvas = page.getByRole('region', { name: '图片编辑画布' })
  await canvas.focus()
  await page.keyboard.press('ArrowRight')
  await expect.poll(() => backend.editorState().revision).toBe(2)
  expect(backend.editorState().document.objects[0].transform[4]).toBe(1)

  const beforeRotate = backend.editorState().document.objects[0].transform
  await page.getByTitle('旋转 90°').click()
  await expect.poll(() => backend.editorState().revision).toBe(3)
  const afterRotate = backend.editorState().document.objects[0].transform
  const center = (matrix: number[]) => ({
    x: matrix[0] * 512 + matrix[2] * 512 + matrix[4],
    y: matrix[1] * 512 + matrix[3] * 512 + matrix[5],
  })
  expect(center(afterRotate).x).toBeCloseTo(center(beforeRotate).x)
  expect(center(afterRotate).y).toBeCloseTo(center(beforeRotate).y)

  await page.locator('input[type="file"]').setInputFiles({
    name: 'editor-layer.png',
    mimeType: 'image/png',
    buffer: Buffer.from('89504e470d0a1a0a', 'hex'),
  })
  await expect.poll(() => backend.editorState().document.objects.length).toBe(2)
  await expect(page.locator('.editor-selection-box')).toBeVisible()

  await expect
    .poll(() => backend.editorState().document.objects[1].name)
    .toBe('editor-layer')
  const layerName = page.locator('.editor-layer-name input')
  await layerName.fill('前景人物')
  await layerName.blur()
  await expect
    .poll(() => backend.editorState().document.objects[1].name)
    .toBe('前景人物')

  await page.getByRole('button', { name: '隐藏图层 前景人物' }).click()
  await expect
    .poll(() => backend.editorState().document.objects[1].visible)
    .toBe(false)
  await page.getByRole('button', { name: '显示图层 前景人物' }).click()
  await page.getByRole('button', { name: '锁定图层 前景人物' }).click()
  await expect
    .poll(() => backend.editorState().document.objects[1].locked)
    .toBe(true)
  await page.getByRole('button', { name: '解锁图层 前景人物' }).click()

  const viewport = await canvas.boundingBox()
  expect(viewport).not.toBeNull()
  await page.mouse.move(viewport!.x + 8, viewport!.y + 8)
  await page.mouse.down()
  await page.mouse.move(
    viewport!.x + viewport!.width - 8,
    viewport!.y + viewport!.height - 8,
  )
  await page.mouse.up()
  await expect(page.getByText('已选 2')).toBeVisible()
  await page.getByRole('button', { name: '选择图层 前景人物' }).click()

  await page
    .getByRole('button', { name: '选择图层 源图' })
    .click({ modifiers: ['Shift'] })
  await expect(page.getByText('已选 2')).toBeVisible()

  const axisScale = (item: { transform: number[] }) =>
    Math.hypot(item.transform[0], item.transform[1])
  const groupCenter = (items: { transform: number[] }[]) => {
    const corners = items.flatMap((item) =>
      [
        [0, 0],
        [1024, 0],
        [1024, 1024],
        [0, 1024],
      ].map(([x, y]) => ({
        x: item.transform[0] * x + item.transform[2] * y + item.transform[4],
        y: item.transform[1] * x + item.transform[3] * y + item.transform[5],
      })),
    )
    const left = Math.min(...corners.map((item) => item.x))
    const right = Math.max(...corners.map((item) => item.x))
    const top = Math.min(...corners.map((item) => item.y))
    const bottom = Math.max(...corners.map((item) => item.y))
    return { x: (left + right) / 2, y: (top + bottom) / 2 }
  }
  const groupBeforeScale = structuredClone(
    backend.editorState().document.objects,
  )
  const scaleHandle = page.locator('.editor-group-selection .is-se')
  const scaleBox = await scaleHandle.boundingBox()
  expect(scaleBox).not.toBeNull()
  await page.mouse.move(
    scaleBox!.x + scaleBox!.width / 2,
    scaleBox!.y + scaleBox!.height / 2,
  )
  await page.mouse.down()
  await page.mouse.move(
    scaleBox!.x + scaleBox!.width / 2 + 72,
    scaleBox!.y + scaleBox!.height / 2 + 72,
  )
  await page.mouse.up()
  await expect
    .poll(() => axisScale(backend.editorState().document.objects[0]))
    .toBeGreaterThan(axisScale(groupBeforeScale[0]))
  const groupAfterScale = backend.editorState().document.objects
  const beforeMidpoint = groupCenter(groupBeforeScale)
  const afterMidpoint = groupCenter(groupAfterScale)
  expect(afterMidpoint.x).toBeCloseTo(beforeMidpoint.x, 4)
  expect(afterMidpoint.y).toBeCloseTo(beforeMidpoint.y, 4)

  const groupBeforeRotate = structuredClone(groupAfterScale)
  const groupBox = await page.locator('.editor-group-selection').boundingBox()
  const rotateHandle = page.locator(
    '.editor-group-selection .editor-rotate-handle',
  )
  const rotateBox = await rotateHandle.boundingBox()
  expect(groupBox).not.toBeNull()
  expect(rotateBox).not.toBeNull()
  const groupScreenCenter = {
    x: groupBox!.x + groupBox!.width / 2,
    y: groupBox!.y + groupBox!.height / 2,
  }
  await page.mouse.move(
    rotateBox!.x + rotateBox!.width / 2,
    rotateBox!.y + rotateBox!.height / 2,
  )
  await page.mouse.down()
  await page.mouse.move(groupScreenCenter.x + 90, groupScreenCenter.y, {
    steps: 8,
  })
  await page.mouse.up()
  await expect
    .poll(() => backend.editorState().document.objects[0].transform[1])
    .not.toBeCloseTo(groupBeforeRotate[0].transform[1], 4)
  const groupAfterRotate = backend.editorState().document.objects
  const rotateCenterBefore = groupCenter(groupBeforeRotate)
  const rotateCenterAfter = groupCenter(groupAfterRotate)
  expect(rotateCenterAfter.x).toBeCloseTo(rotateCenterBefore.x, 4)
  expect(rotateCenterAfter.y).toBeCloseTo(rotateCenterBefore.y, 4)
  const beforeGroupMove = backend
    .editorState()
    .document.objects.map((item) => item.transform[4])
  await canvas.focus()
  await page.keyboard.press('ArrowRight')
  await expect
    .poll(() =>
      backend.editorState().document.objects.map((item) => item.transform[4]),
    )
    .toEqual(beforeGroupMove.map((value) => value + 1))
  await page.keyboard.press('Control+d')
  await expect.poll(() => backend.editorState().document.objects.length).toBe(4)
  await page.keyboard.press('Control+z')
  await expect.poll(() => backend.editorState().document.objects.length).toBe(2)
  await page.getByRole('button', { name: '选择图层 前景人物' }).click()

  const centerX = page.locator('.editor-geometry-grid input').nth(0)
  const centerY = page.locator('.editor-geometry-grid input').nth(1)
  await centerX.fill('600')
  await centerY.fill('450')
  await expect
    .poll(() => {
      const object = backend.editorState().document.objects[1]
      return (
        object.transform[0] * 512 +
        object.transform[2] * 512 +
        object.transform[4]
      )
    })
    .toBeCloseTo(600, 4)
  await expect
    .poll(() => {
      const object = backend.editorState().document.objects[1]
      return (
        object.transform[1] * 512 +
        object.transform[3] * 512 +
        object.transform[5]
      )
    })
    .toBeCloseTo(450, 4)

  const worldBeforeWheel = await page
    .locator('.editor-world')
    .getAttribute('style')
  await canvas.hover()
  await page.mouse.wheel(30, 45)
  await expect(page.locator('.editor-world')).not.toHaveAttribute(
    'style',
    worldBeforeWheel ?? '',
  )

  await page.reload()
  await expect(page.getByRole('textbox', { name: '工程名称' })).toHaveValue(
    'Asset 0',
  )
  await expect(page.getByText('已保存')).toBeVisible()
})

test.skip('legacy V1 editor crops a rotated layer with explicit apply and cancel', async ({
  page,
}) => {
  const backend = await installStudioMocks(page)
  await page.goto('/app/editor/editor-project-1')
  const canvas = page.getByRole('region', { name: '图片编辑画布' })
  await expect(canvas).toBeVisible()

  await page.getByTitle('旋转 90°').click()
  await expect.poll(() => backend.editorState().revision).toBe(1)
  const transformBeforeCrop = structuredClone(
    backend.editorState().document.objects[0].transform,
  )

  await page.getByRole('button', { name: '裁切图层' }).click()
  await expect(page.getByRole('toolbar', { name: '裁切操作' })).toBeVisible()
  await expect(
    page.getByRole('button', { name: '智能分层', exact: true }),
  ).toBeDisabled()
  const eastHandle = page.getByRole('button', { name: '调整裁切区域 e' })
  const eastBox = await eastHandle.boundingBox()
  expect(eastBox).not.toBeNull()
  await page.mouse.move(
    eastBox!.x + eastBox!.width / 2,
    eastBox!.y + eastBox!.height / 2,
  )
  await page.mouse.down()
  await page.mouse.move(
    eastBox!.x + eastBox!.width / 2,
    eastBox!.y + eastBox!.height / 2 - 90,
    { steps: 8 },
  )
  await page.mouse.up()
  const draftWidth = await page
    .locator('.editor-crop-frame')
    .evaluate((element) =>
      Number.parseFloat((element as HTMLElement).style.width),
    )
  expect(draftWidth).toBeLessThan(1024)
  expect(backend.editorState().document.objects[0].crop).toBeUndefined()

  await canvas.focus()
  await page.keyboard.press('Escape')
  await expect(page.getByRole('toolbar', { name: '裁切操作' })).toBeHidden()
  expect(backend.editorState().document.objects[0].crop).toBeUndefined()
  expect(backend.editorState().document.objects[0].transform).toEqual(
    transformBeforeCrop,
  )
  expect(backend.editorState().revision).toBe(1)

  await page.getByRole('button', { name: '裁切图层' }).click()
  const secondEastBox = await page
    .getByRole('button', { name: '调整裁切区域 e' })
    .boundingBox()
  expect(secondEastBox).not.toBeNull()
  await page.mouse.move(
    secondEastBox!.x + secondEastBox!.width / 2,
    secondEastBox!.y + secondEastBox!.height / 2,
  )
  await page.mouse.down()
  await page.mouse.move(
    secondEastBox!.x + secondEastBox!.width / 2,
    secondEastBox!.y + secondEastBox!.height / 2 - 120,
    { steps: 8 },
  )
  await page.mouse.up()
  await canvas.focus()
  await page.keyboard.press('Enter')
  await expect.poll(() => backend.editorState().revision).toBe(2)
  expect(backend.editorState().document.objects[0].crop?.width).toBeLessThan(1)
  await expect(page.getByText('裁切区域')).toBeVisible()

  await canvas.focus()
  await page.keyboard.press('Control+z')
  await expect.poll(() => backend.editorState().revision).toBe(3)
  expect(backend.editorState().document.objects[0].crop).toBeUndefined()
  await page.keyboard.press('Control+Shift+z')
  await expect.poll(() => backend.editorState().revision).toBe(4)
  expect(backend.editorState().document.objects[0].crop?.width).toBeLessThan(1)

  await page.reload()
  await expect(page.getByText('裁切区域')).toBeVisible()
  expect(backend.editorState().document.objects[0].crop?.width).toBeLessThan(1)
})

test.skip('legacy V1 editor keeps DOM as the default renderer and mounts Pixi only when requested', async ({
  page,
}) => {
  await installStudioMocks(page)
  await page.route('**/mock-image.svg*', (route) =>
    route.fulfill({
      status: 200,
      contentType: 'image/webp',
      body: readFileSync(
        new URL('../public/cornfield-chair.webp', import.meta.url),
      ),
    }),
  )
  await page.goto('/app/editor/editor-project-1')
  await expect(page.getByTestId('editor-pixi-surface')).toHaveCount(0)
  await expect(page.locator('.editor-canvas > img')).toHaveCSS('opacity', '1')

  await page.goto('/app/editor/editor-project-1?renderer=pixi')
  const surface = page.getByTestId('editor-pixi-surface')
  await expect(surface).toBeVisible()
  await expect
    .poll(() =>
      surface.evaluate((canvas: HTMLCanvasElement) => ({
        ready: canvas.width > 0 && canvas.height > 0,
      })),
    )
    .toEqual({ ready: true })
  await expect(page.locator('.editor-canvas > img')).toHaveCount(0)
  await expect(page.getByTestId('editor-pixi-artboard-underlay')).toBeVisible()
  await expect(page.locator('.editor-object-hit')).toHaveCount(1)
  await expect(page.locator('.editor-selection-box')).toBeVisible()

  await page.getByTestId('editor-crop-tool').click()
  await expect(surface).toHaveCount(0)
  await expect(page.getByTestId('editor-pixi-artboard-underlay')).toHaveCount(0)
  await expect(page.locator('.editor-canvas > img')).toHaveCSS('opacity', '1')
})

test.skip('legacy V1 editor aligns and distributes multiple layers as one undoable edit', async ({
  page,
}) => {
  const backend = await installStudioMocks(page)
  await page.goto('/app/editor/editor-project-1')
  const canvas = page.getByRole('region', { name: '图片编辑画布' })
  await expect(canvas).toBeVisible()

  await canvas.focus()
  await page.keyboard.press('Control+d')
  await expect.poll(() => backend.editorState().document.objects.length).toBe(2)
  await page.keyboard.press('Control+d')
  await expect.poll(() => backend.editorState().document.objects.length).toBe(3)

  const setCenterX = async (layerName: string, value: number) => {
    await page
      .getByRole('button', { name: `选择图层 ${layerName}`, exact: true })
      .click()
    const input = page.locator('.editor-geometry-grid input').nth(0)
    await input.fill(String(value))
    await input.blur()
    await expect
      .poll(() => {
        const object = backend
          .editorState()
          .document.objects.find((item) => item.name === layerName)!
        return (
          object.transform[0] * 512 +
          object.transform[2] * 512 +
          object.transform[4]
        )
      })
      .toBeCloseTo(value, 4)
  }
  const centers = () =>
    backend
      .editorState()
      .document.objects.map(
        (object) =>
          object.transform[0] * 512 +
          object.transform[2] * 512 +
          object.transform[4],
      )
      .sort((a, b) => a - b)

  await setCenterX('源图', 100)
  await setCenterX('源图 副本', 240)
  await setCenterX('源图 副本 副本', 900)
  await canvas.focus()
  await page.keyboard.press('Control+a')
  await expect(page.getByText('已选 3')).toBeVisible()

  await page.getByRole('button', { name: '水平等距分布所选图层' }).click()
  await expect.poll(centers).toEqual([100, 500, 900])
  await canvas.focus()
  await page.keyboard.press('Control+z')
  await expect.poll(centers).toEqual([100, 240, 900])

  await page.getByRole('button', { name: '左对齐所选图层' }).click()
  await expect
    .poll(() => {
      const lefts = backend
        .editorState()
        .document.objects.map((object) => object.transform[4])
      return Math.max(...lefts) - Math.min(...lefts)
    })
    .toBeLessThan(1e-4)
  await canvas.focus()
  await page.keyboard.press('Control+z')
  await expect.poll(centers).toEqual([100, 240, 900])

  await page.getByRole('button', { name: '选择图层 源图', exact: true }).click()
  await page.getByRole('button', { name: '锁定图层 源图', exact: true }).click()
  const lockedTransform = structuredClone(
    backend.editorState().document.objects[0].transform,
  )
  await canvas.focus()
  await page.keyboard.press('Control+a')
  await page.getByRole('button', { name: '右对齐所选图层' }).click()
  await expect
    .poll(() => backend.editorState().document.objects[1].transform[4])
    .toBeCloseTo(lockedTransform[4], 4)
  expect(backend.editorState().document.objects[0].transform).toEqual(
    lockedTransform,
  )
})

test('V3 editor autosaves artboards and restores them after reload', async ({
  page,
}) => {
  const backend = await installStudioMocks(page)
  const pageErrors: string[] = []
  const consoleErrors: string[] = []
  page.on('pageerror', (error) => pageErrors.push(error.message))
  page.on('console', (message) => {
    if (message.type() === 'error') consoleErrors.push(message.text())
  })
  await page.goto('/app/editor/editor-project-1')

  await expect(page.getByTestId('editor-dom-surface')).toBeVisible()
  await expect(page.getByRole('region', { name: '图片编辑画布' })).toBeVisible()
  await expect(page.getByRole('option', { name: /1024×1024/ })).toBeVisible()

  const name = page.getByRole('textbox', { name: '画板名称' })
  await name.fill('主画板')
  await name.blur()
  await expect.poll(() => backend.editorState().revision).toBeGreaterThan(0)
  expect(backend.editorState().document.schema_version).toBe(3)
  expect(backend.editorState().document.artboards[0].name).toBe('主画板')

  await page.getByRole('button', { name: '新建空白画板' }).click()
  const creator = page.locator('.structured-new-artboard')
  await creator.locator('input').nth(0).fill('1600')
  await creator.locator('input').nth(1).fill('900')
  await creator.getByRole('button', { name: '创建画板' }).click()
  await expect.poll(() => pageErrors).toEqual([])
  await expect
    .poll(() =>
      consoleErrors.filter((message) =>
        message.includes('Invalid editor document'),
      ),
    )
    .toEqual([])
  await expect
    .poll(() => backend.editorState().document.artboards.length)
    .toBe(2)
  await expect(page.getByRole('option', { name: /1600×900/ })).toBeVisible()

  await page.reload()
  await expect(page.getByRole('option', { name: /主画板/ })).toBeVisible()
  await expect(page.getByRole('option', { name: /1600×900/ })).toBeVisible()
})

test('V3 editor uses one CSP-safe workbench with a non-blocking fallback', async ({
  page,
}) => {
  await installStudioMocks(page)
  const pageErrors: string[] = []
  page.on('pageerror', (error) => pageErrors.push(error.message))
  await page.goto('/app/editor/editor-project-1')

  await expect(page.getByTestId('editor-pixi-surface')).toBeVisible()
  await expect(page.getByRole('button', { name: '基础模式' })).toHaveCount(0)
  await expect(page.getByRole('button', { name: '专业模式' })).toHaveCount(0)
  await expect(page.getByRole('navigation', { name: '画布工具' })).toBeVisible()
  await expect(page.getByRole('button', { name: '智能分层' })).toBeVisible()
  await expect(
    page.getByRole('separator', { name: '调整侧栏宽度' }),
  ).toBeVisible()
  await expect(
    page.getByRole('separator', { name: '调整图层与属性面板高度' }),
  ).toBeVisible()
  expect(pageErrors.some((message) => message.includes('unsafe-eval'))).toBe(
    false,
  )
})

test('V3 editor imports every image as an independent artboard', async ({
  page,
}) => {
  const backend = await installStudioMocks(page)
  await page.goto('/app/editor/editor-project-1')

  await page
    .locator('.structured-artboard-panel input[type="file"]')
    .setInputFiles({
      name: 'reference-board.png',
      mimeType: 'image/png',
      buffer: Buffer.from('89504e470d0a1a0a', 'hex'),
    })

  await expect
    .poll(() => backend.editorState().document.artboards.length)
    .toBe(2)
  const imported = backend.editorState().document.artboards[1]
  expect(imported.name).toBe('reference-board')
  expect(imported.nodes).toHaveLength(1)
  expect(imported.nodes[0].asset_id).toBe('asset-editor-upload')
  expect(backend.editorState().document.artboards[0].nodes).toHaveLength(1)
  await expect(
    page.getByRole('option', { name: /reference-board/ }),
  ).toBeVisible()
})

test.describe('mobile studio', () => {
  test.use({ viewport: { width: 390, height: 844 }, hasTouch: true })

  test('keeps a rounded, point-free generate action', async ({
    page,
  }, testInfo) => {
    const errors = capturePageErrors(page)
    await installStudioMocks(page)
    await page.goto('/app/create')

    const generate = page.locator('.generate-button')
    await expect(generate).toHaveText('生成')
    await expect(generate).not.toContainText(/积分|points?|credits?|\d/i)

    const radii = await generate.evaluate((button) => ({
      button: Number.parseFloat(getComputedStyle(button).borderTopLeftRadius),
      generator: Number.parseFloat(
        getComputedStyle(button.closest('.generator')!).borderTopLeftRadius,
      ),
    }))
    expect(radii.button).toBeGreaterThanOrEqual(8)
    expect(radii.generator).toBeGreaterThanOrEqual(8)

    await page.getByRole('article', { name: '打开生成图片预览' }).first().tap()
    await expect(page.getByRole('dialog', { name: '图片预览' })).toBeVisible()
    await page.getByRole('button', { name: '关闭预览' }).click()
    await expect(page.getByRole('dialog', { name: '图片预览' })).toBeHidden()
    await page.screenshot({ path: testInfo.outputPath('mobile-studio.png') })
    expect(errors).toEqual([])
  })
})

function capturePageErrors(page: Page) {
  const errors: string[] = []
  page.on('console', (message) => {
    if (message.type() === 'error') errors.push(message.text())
  })
  page.on('pageerror', (error) => errors.push(error.message))
  return errors
}

async function installStudioMocks(
  page: Page,
  options: {
    user?: {
      id: string
      username: string
      display_name: string
      role: 'member' | 'admin'
      must_change_password: boolean
    }
    generationPages?: Record<string, { items: unknown[]; next_cursor: string }>
    generationPostNetworkFailures?: number
    refinerDelayMs?: number
    refinerFailure?: { status: number; code: string; message: string }
    feedbackFails?: boolean
    models?: unknown[]
  } = {},
) {
  let assets = Array.from({ length: 18 }, (_, index) => ({
    id: `asset-${index}`,
    kind: 'generation',
    media_type: 'image/webp',
    width: index % 3 === 0 ? 1024 : index % 3 === 1 ? 1280 : 900,
    height: index % 3 === 0 ? 1024 : index % 3 === 1 ? 720 : 1200,
    byte_size: 1_000,
    sha256: `hash-${index}`,
    url: `/mock-image.svg?asset=${index}`,
    thumb_320_url: `/mock-image.svg?asset=${index}&size=320`,
    thumb_640_url: `/mock-image.svg?asset=${index}&size=640`,
    thumb_1280_url: `/mock-image.svg?asset=${index}&size=1280`,
    created_at: new Date(Date.now() - index * 1_000).toISOString(),
  }))
  let hiddenAssets: typeof assets = []
  let generations: unknown[] = []
  let folders: {
    id: string
    name: string
    asset_count: number
    created_at: string
  }[] = []
  let revoked = false
  let postAttempts = 0
  let uploadAttempts = 0
  let lastUploadPurpose = ''
  let lastGenerationInput: { input_asset_ids?: string[] } | undefined
  let refineAttempts = 0
  const refinementFeedback: Array<{
    refinementID: string
    event: string
    batch_id?: string
  }> = []
  const postKeys: string[] = []
  let editorRevision = 0
  let editorUploadReady = false
  let editorDocument: EditorDocumentV3 = {
    schema_version: 3 as const,
    renderer_semantics_version: 2 as const,
    active_artboard_id: 'artboard-1',
    artboards: [
      {
        id: 'artboard-1',
        name: '画板 1',
        order_key: '000001',
        x: 0,
        y: 0,
        width: 1024,
        height: 1024,
        visible: true,
        locked: false,
        nodes: [
          {
            id: 'source',
            type: 'raster' as const,
            name: '源图',
            parent_id: null,
            order_key: '000001',
            asset_id: 'asset-0',
            transform: [1, 0, 0, 1, 0, 0] as [
              number,
              number,
              number,
              number,
              number,
              number,
            ],
            opacity: 1,
            blend_mode: 'normal' as const,
            visible: true,
            locked: false,
            effects: [],
          },
        ],
      },
    ],
  }

  await page.route('**/mock-image.svg*', (route) =>
    route.fulfill({
      status: 200,
      contentType: 'image/svg+xml',
      body: mockImage,
    }),
  )
  await page.route('**/api/v1/**', async (route) => {
    const request = route.request()
    const url = new URL(request.url())
    const { pathname } = url
    if (revoked) {
      return json(
        route,
        { error: { code: 'UNAUTHORIZED', message: '登录已失效' } },
        401,
      )
    }
    if (pathname === '/api/v1/auth/me') {
      return json(route, {
        user: options.user ?? {
          id: 'qa-user',
          username: 'qa',
          display_name: 'QA',
          role: 'admin',
          must_change_password: false,
        },
      })
    }
    if (pathname === '/api/v1/auth/logout' && request.method() === 'POST') {
      return route.fulfill({ status: 204 })
    }
    if (pathname === '/api/v1/uploads' && request.method() === 'POST') {
      uploadAttempts++
      editorUploadReady = false
      const input = request.postDataJSON() as { purpose?: string }
      lastUploadPurpose = input.purpose ?? 'library'
      const uploaded = {
        ...assets[0],
        id: 'asset-editor-upload',
        kind: 'upload',
        original_filename: 'editor-layer.png',
        created_at: new Date().toISOString(),
      }
      if (lastUploadPurpose === 'reference')
        hiddenAssets = [uploaded, ...hiddenAssets]
      else assets = [uploaded, ...assets]
      return json(
        route,
        {
          id: 'upload-editor',
          status: 'created',
          content_url: '/api/v1/uploads/upload-editor/content',
        },
        201,
      )
    }
    if (
      pathname === '/api/v1/uploads/upload-editor/content' &&
      request.method() === 'PUT'
    ) {
      editorUploadReady = true
      return route.fulfill({ status: 204 })
    }
    if (
      pathname === '/api/v1/uploads/upload-editor' &&
      request.method() === 'GET'
    ) {
      return json(route, {
        status: editorUploadReady ? 'ready' : 'validating',
        asset_id: editorUploadReady ? 'asset-editor-upload' : undefined,
      })
    }
    if (pathname === '/api/v1/models') {
      return json(route, {
        revision: 'qa-revision',
        models: options.models ?? [
          {
            id: 'nano-banana-pro',
            display_name: 'Nano Banana Pro',
            provider: 'openrouter',
            outputs_per_draw: 1,
            availability: { state: 'healthy', can_submit: true },
            capabilities: {
              text_to_image: true,
              image_to_image: true,
              aspect_ratios: ['1:1', '3:4', '16:9'],
              resolutions: ['1K', '2K'],
              max_reference_images: 4,
              max_reference_bytes: 26214400,
              draw_count: { min: 1, max: 4, default: 1 },
            },
          },
        ],
      })
    }
    if (pathname === '/api/v1/prompts/refine' && request.method() === 'POST') {
      refineAttempts++
      const input = request.postDataJSON() as { prompt: string }
      if (options.refinerDelayMs)
        await new Promise((resolve) =>
          setTimeout(resolve, options.refinerDelayMs),
        )
      if (options.refinerFailure) {
        return json(
          route,
          {
            error: {
              code: options.refinerFailure.code,
              message: options.refinerFailure.message,
            },
          },
          options.refinerFailure.status,
        )
      }
      if (input.prompt === 'clean-check') {
        return json(route, {
          policy_version: '2026-08-26.1',
          refinement_id: `refinement-${refineAttempts}`,
          optimized_prompt: input.prompt,
          changed: false,
          diagnostics: [],
        })
      }
      return json(route, {
        policy_version: '2026-08-26.1',
        refinement_id: `refinement-${refineAttempts}`,
        optimized_prompt: 'crimson liquid over a quiet cornfield',
        changed: true,
        diagnostics: [],
      })
    }
    if (
      pathname === '/api/v1/prompts/refinements/feedback' &&
      request.method() === 'POST'
    ) {
      const input = request.postDataJSON() as {
        refinement_id: string
        event: string
        batch_id?: string
      }
      refinementFeedback.push({
        refinementID: input.refinement_id,
        event: input.event,
        batch_id: input.batch_id,
      })
      if (options.feedbackFails)
        return json(
          route,
          { error: { code: 'FEEDBACK_FAILED', message: 'temporarily down' } },
          500,
        )
      return route.fulfill({ status: 204 })
    }
    if (pathname === '/api/v1/assets') {
      const view = url.searchParams.get('view') ?? 'active'
      const folderID = url.searchParams.get('folder_id')
      const visible = assets.filter((asset) => {
        const organized = asset as typeof asset & {
          folder_id?: string
          archived_at?: string
        }
        const inView =
          view === 'all' ||
          (view === 'active' && !organized.archived_at) ||
          (view === 'archived' && !!organized.archived_at)
        return inView && (!folderID || organized.folder_id === folderID)
      })
      return json(route, { items: visible, next_cursor: '' })
    }
    const assetMatch = pathname.match(/^\/api\/v1\/assets\/([^/]+)$/)
    if (assetMatch && request.method() === 'GET') {
      const asset = [...assets, ...hiddenAssets].find(
        (item) => item.id === assetMatch[1],
      )
      return asset
        ? json(route, asset)
        : json(route, { error: { code: 'ASSET_NOT_FOUND' } }, 404)
    }
    if (
      pathname === '/api/v1/assets/asset-0/editor-project' &&
      request.method() === 'POST'
    ) {
      return json(
        route,
        {
          id: 'editor-project-1',
          source_asset_id: 'asset-0',
          name: 'Asset 0',
          document: editorDocument,
          revision: editorRevision,
          created_at: new Date().toISOString(),
          updated_at: new Date().toISOString(),
        },
        201,
      )
    }
    if (
      pathname === '/api/v1/editor-projects/editor-project-1' &&
      request.method() === 'GET'
    ) {
      return json(route, {
        id: 'editor-project-1',
        source_asset_id: 'asset-0',
        name: 'Asset 0',
        document: editorDocument,
        revision: editorRevision,
        created_at: new Date().toISOString(),
        updated_at: new Date().toISOString(),
      })
    }
    if (
      pathname === '/api/v1/editor-projects/editor-project-1/document' &&
      request.method() === 'PUT'
    ) {
      const input = request.postDataJSON() as {
        expected_revision: number
        document: typeof editorDocument
      }
      if (input.expected_revision !== editorRevision)
        return json(
          route,
          { error: { code: 'EDITOR_PROJECT_CONFLICT', message: 'conflict' } },
          409,
        )
      editorDocument = input.document
      editorRevision++
      return json(route, { revision: editorRevision })
    }
    if (
      pathname === '/api/v1/editor-projects/editor-project-1' &&
      request.method() === 'PATCH'
    ) {
      return route.fulfill({ status: 204 })
    }
    if (pathname === '/api/v1/asset-folders' && request.method() === 'GET') {
      return json(route, { items: folders })
    }
    if (pathname === '/api/v1/asset-folders' && request.method() === 'POST') {
      const input = request.postDataJSON() as { name: string }
      const folder = {
        id: `folder-${folders.length + 1}`,
        name: input.name,
        asset_count: 0,
        created_at: new Date().toISOString(),
      }
      folders = [...folders, folder]
      return json(route, folder, 201)
    }
    if (pathname.endsWith('/organization') && request.method() === 'PATCH') {
      const assetID = pathname.split('/').at(-2)
      const input = request.postDataJSON() as {
        folder_id?: string | null
        archived?: boolean
      }
      assets = assets.map((asset) =>
        asset.id === assetID
          ? {
              ...asset,
              ...(input.folder_id !== undefined
                ? { folder_id: input.folder_id ?? undefined }
                : {}),
              ...(input.archived !== undefined
                ? {
                    archived_at: input.archived
                      ? new Date().toISOString()
                      : undefined,
                  }
                : {}),
            }
          : asset,
      )
      folders = folders.map((folder) => ({
        ...folder,
        asset_count: assets.filter(
          (asset) =>
            (asset as typeof asset & { folder_id?: string }).folder_id ===
            folder.id,
        ).length,
      }))
      return route.fulfill({ status: 204 })
    }
    if (pathname === '/api/v1/generations' && request.method() === 'GET') {
      const configuredPage =
        options.generationPages?.[url.searchParams.get('cursor') ?? '']
      return json(
        route,
        configuredPage ?? { items: generations, next_cursor: '' },
      )
    }
    const generationMatch = pathname.match(/^\/api\/v1\/generations\/([^/]+)$/)
    if (generationMatch && request.method() === 'GET') {
      const configured = Object.values(options.generationPages ?? {}).flatMap(
        (generationPage) => generationPage.items,
      )
      const batch = [...configured, ...generations].find(
        (item) =>
          typeof item === 'object' &&
          item !== null &&
          'id' in item &&
          item.id === generationMatch[1],
      )
      return batch
        ? json(route, batch)
        : json(
            route,
            { error: { code: 'NOT_FOUND', message: 'not found' } },
            404,
          )
    }
    if (pathname === '/api/v1/generations' && request.method() === 'POST') {
      postAttempts++
      postKeys.push(request.headers()['idempotency-key'] ?? '')
      if (postAttempts <= (options.generationPostNetworkFailures ?? 0)) {
        return route.abort('connectionreset')
      }
      const input = request.postDataJSON() as {
        model_id: string
        prompt: string
        aspect_ratio: string
        resolution: string
        draw_count: number
        input_asset_ids?: string[]
      }
      lastGenerationInput = input
      await new Promise((resolve) => setTimeout(resolve, 2_000))
      const batch = {
        id: 'batch-qa',
        model_id: input.model_id,
        prompt: input.prompt,
        aspect_ratio: input.aspect_ratio,
        resolution: input.resolution,
        draw_count: input.draw_count,
        expected_outputs: input.draw_count,
        completed_outputs: 0,
        status: 'queued',
        created_at: new Date().toISOString(),
        jobs: Array.from({ length: input.draw_count }, (_, index) => ({
          id: `job-qa-${index}`,
          draw_index: index,
          status: 'queued',
          expected_outputs: 1,
          outputs: [],
        })),
      }
      generations = [batch]
      return json(route, batch, 201)
    }
    if (pathname.endsWith('/cancel') && request.method() === 'POST') {
      return json(
        route,
        {
          status: 'cancelling',
          cancel_mode: 'discard_result_only',
          cost_may_have_been_incurred: true,
        },
        202,
      )
    }
    if (pathname === '/api/v1/events') {
      return route.fulfill({
        status: 200,
        contentType: 'text/event-stream',
        headers: { 'cache-control': 'no-cache' },
        body: ': connected\n\n',
      })
    }
    return json(
      route,
      { error: { code: 'E2E_UNMOCKED', message: pathname } },
      404,
    )
  })
  return {
    revoke: () => {
      revoked = true
    },
    setGenerations: (items: unknown[]) => {
      generations = items
    },
    prependAsset: (asset: (typeof assets)[number]) => {
      assets = [asset, ...assets]
    },
    postAttempts: () => postAttempts,
    postKeys: () => [...postKeys],
    uploadAttempts: () => uploadAttempts,
    lastUploadPurpose: () => lastUploadPurpose,
    lastGenerationInput: () => lastGenerationInput,
    refineAttempts: () => refineAttempts,
    refinementFeedback: () => [...refinementFeedback],
    editorState: () => ({
      revision: editorRevision,
      document: editorDocument as unknown as {
        schema_version: number
        canvas: { width: number; height: number }
        objects: Array<{
          id: string
          name: string
          asset_id: string
          transform: number[]
          opacity: number
          visible: boolean
          locked: boolean
          crop?: { x: number; y: number; width: number; height: number }
        }>
        artboards: EditorDocumentV3['artboards']
      },
    }),
  }
}

function json(
  route: Parameters<Parameters<Page['route']>[1]>[0],
  body: unknown,
  status = 200,
) {
  return route.fulfill({
    status,
    contentType: 'application/json',
    body: JSON.stringify(body),
  })
}
