import assert from 'node:assert/strict'
import test from 'node:test'
import { releaseMetadata } from './release-metadata.mjs'

const input = {
  sha: '61185489a653f9f96a31ce2793c133d6308ceb57',
  ref: 'refs/heads/main',
  runId: '35802758494',
  version: 'v1.0.0-rc.4',
}

test('主分支构建生成独立 Release 和本地镜像身份', () => {
  const result = releaseMetadata(input)
  assert.equal(result.image, 'rillven:sha-61185489a653')
  assert.equal(result.release_tag, 'rillven-build-61185489a653-35802758494')
  assert.equal(result.build_channel, 'edge')
  assert.equal(result.prerelease, 'true')
  assert.equal(result.make_latest, 'false')
  assert.notEqual(result.release_tag, releaseMetadata({ ...input, runId: '35802758495' }).release_tag)
})

test('正式版本与候选版本保留发布语义', () => {
  const candidate = releaseMetadata({ ...input, ref: 'refs/tags/rillven-v1.0.0-rc.4' })
  assert.equal(candidate.release_tag, 'rillven-v1.0.0-rc.4')
  assert.equal(candidate.prerelease, 'true')
  const stable = releaseMetadata({ ...input, ref: 'refs/tags/rillven-v1.0.0', version: 'v1.0.0' })
  assert.equal(stable.image, 'rillven:1.0.0')
  assert.equal(stable.release_name, 'Rillven v1.0.0')
  assert.equal(stable.prerelease, 'false')
  assert.equal(stable.make_latest, 'true')
})

test('拒绝错误源码、版本冲突和危险标签', () => {
  for (const override of [
    { expectedSha: '0'.repeat(40) },
    { ref: 'refs/tags/renewapi-v1.0.0-rc.4' },
    { ref: 'refs/tags/v1.0.0-rc.4' },
    { releaseTag: 'renewapi-source-check' },
    { sha: 'main' },
    { runId: '1\nimage=other' },
    { runId: '123\n' },
    { version: 'v1.0.0\nnext=value' },
    { ref: 'refs/tags/rillven-v1.0.0-rc.3' },
    { imageTag: 'latest' },
    { imageTag: 'edge' },
    { imageTag: 'other/image' },
    { imageTag: 'source\n' },
    { releaseTag: 'rillven-v1.0.0' },
    { releaseTag: 'v1.0.0-rc.4' },
    { releaseTag: 'rillven-build-foo\nbar' },
    { releaseTag: 'rillven-build-foo\n' },
    { ref: 'refs/tags/rillven-v1.0.0-rc.4', imageTag: 'custom' },
  ]) {
    assert.throws(() => releaseMetadata({ ...input, ...override }))
  }
})

test('手动源码包保留受限的自定义身份', () => {
  const result = releaseMetadata({
    ...input,
    ref: 'refs/heads/fix/release',
    imageTag: 'source-check',
    releaseTag: 'rillven-source-check',
  })
  assert.equal(result.image, 'rillven:source-check')
  assert.equal(result.release_tag, 'rillven-source-check')
  assert.equal(result.build_channel, 'manual')
  assert.equal(result.make_latest, 'false')
})
