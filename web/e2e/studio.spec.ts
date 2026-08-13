import { expect, test } from '@playwright/test'
import type { Page } from '@playwright/test'

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

test('prompt refiner is manual, selective, and undoable', async ({ page }) => {
  const studio = await installStudioMocks(page)
  await page.goto('/app/create')
  const prompt = page.getByRole('textbox', { name: '生成提示词' })
  await prompt.fill('blood over a quiet cornfield')
  expect(studio.refineAttempts()).toBe(0)

  await page.getByRole('button', { name: '检查并优化提示词' }).click()
  const dialog = page.getByRole('dialog', { name: '检查并优化提示词' })
  await expect(dialog).toBeVisible()
  await expect(dialog.locator('mark')).toHaveText('blood')
  const finding = dialog.getByRole('checkbox')
  await expect(finding).not.toBeChecked()
  await finding.check()
  await dialog.getByRole('button', { name: '应用所选修改' }).click()

  await expect(prompt).toHaveValue('crimson liquid over a quiet cornfield')
  await expect(page.getByText('已应用 1 项修改')).toBeVisible()
  await page.getByRole('button', { name: '撤销' }).click()
  await expect(prompt).toHaveValue('blood over a quiet cornfield')
  await prompt.fill('clean-check')
  await page.getByRole('button', { name: '检查并优化提示词' }).click()
  await expect(
    page.getByText('未发现需要处理的内容，可以保持原文继续创作。'),
  ).toBeVisible()
  await page.getByRole('button', { name: '保持原文' }).click()
  expect(studio.refineAttempts()).toBe(2)
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

test('image editor restores a source project and autosaves keyboard edits', async ({
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

  const centerX = page.locator('.editor-geometry-grid input').nth(0)
  const centerY = page.locator('.editor-geometry-grid input').nth(1)
  await centerX.fill('600')
  await centerY.fill('450')
  await expect
    .poll(() => {
      const object = backend.editorState().document.objects[1]
      return {
        x:
          object.transform[0] * 512 +
          object.transform[2] * 512 +
          object.transform[4],
        y:
          object.transform[1] * 512 +
          object.transform[3] * 512 +
          object.transform[5],
      }
    })
    .toEqual({ x: 600, y: 450 })

  await canvas.focus()
  await page.keyboard.press('Control+d')
  await expect.poll(() => backend.editorState().document.objects.length).toBe(3)
  await page.keyboard.press('Control+z')
  await expect.poll(() => backend.editorState().document.objects.length).toBe(2)

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
  let generations: unknown[] = []
  let folders: {
    id: string
    name: string
    asset_count: number
    created_at: string
  }[] = []
  let revoked = false
  let postAttempts = 0
  let refineAttempts = 0
  const postKeys: string[] = []
  let editorRevision = 0
  let editorUploadReady = false
  let editorDocument = {
    schema_version: 1 as const,
    canvas: { width: 1024, height: 1024 },
    objects: [
      {
        id: 'source',
        name: '源图',
        asset_id: 'asset-0',
        transform: [1, 0, 0, 1, 0, 0],
        opacity: 1,
        visible: true,
        locked: false,
        z_index: 0,
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
      editorUploadReady = false
      const uploaded = {
        ...assets[0],
        id: 'asset-editor-upload',
        kind: 'upload',
        original_filename: 'editor-layer.png',
        created_at: new Date().toISOString(),
      }
      assets = [uploaded, ...assets]
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
      if (input.prompt === 'clean-check') {
        return json(route, {
          policy_version: '2026-07-29.1',
          status: 'clean',
          segments: [{ text: input.prompt }],
          findings: [],
          diagnostics: [],
        })
      }
      return json(route, {
        policy_version: '2026-07-29.1',
        status: 'findings',
        segments: [
          { text: 'blood', finding_id: 'gore.blood.en:0' },
          { text: ' over a quiet cornfield' },
        ],
        findings: [
          {
            id: 'gore.blood.en:0',
            locale: 'en',
            category: 'gore',
            mode: 'mapped',
            original: 'blood',
            reason: '可能触发血腥内容审核',
            replacements: ['crimson liquid', 'dramatic red accents'],
          },
        ],
        diagnostics: [],
      })
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
      const asset = assets.find((item) => item.id === assetMatch[1])
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
      }
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
    refineAttempts: () => refineAttempts,
    editorState: () => ({ revision: editorRevision, document: editorDocument }),
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
