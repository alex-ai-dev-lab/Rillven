import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import { api } from '@/lib/api'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'

type IdentityView = {
  revision: number
  installation_id: string
  device_id: string
  multi_key: boolean
  slots: Array<{ slot_id: string; installation_id: string; device_id: string }>
}

export function ChannelIdentityEditor({ channelId }: { channelId?: number }) {
  const { t } = useTranslation()
  const [value, setValue] = useState<IdentityView | null>(null)
  const [busy, setBusy] = useState(false)
  const endpoint = `/api/channel/${channelId}/client_identity`

  const load = async () => {
    const { data: result } = await api.post<{
      success: boolean
      data: IdentityView
      message?: string
    }>(`${endpoint}/preview`, {})
    if (!result.success)
      throw new Error(result.message || t('Failed to load channel identity'))
    setValue(result.data)
  }
  const perform = async (action: () => Promise<void>) => {
    if (busy) return
    setBusy(true)
    try {
      await action()
    } catch (error) {
      toast.error(
        error instanceof Error
          ? error.message
          : t('Failed to update channel identity')
      )
    } finally {
      setBusy(false)
    }
  }
  const save = async (reset: boolean) => {
    if (!value) return
    const { data: result } = await api.put<{
      success: boolean
      message?: string
    }>(endpoint, {
      revision: value.revision,
      installation_id: value.installation_id,
      device_id: value.device_id,
      reset,
    })
    if (!result.success)
      throw new Error(result.message || t('Failed to update channel identity'))
    await load()
    toast.success(t('Channel identity saved'))
  }

  if (!channelId)
    return (
      <p className='text-muted-foreground text-sm'>
        {t('Save the channel before configuring its device identity.')}
      </p>
    )
  return (
    <div className='space-y-3 rounded-md border p-3'>
      <Button
        type='button'
        variant='outline'
        disabled={busy}
        onClick={() => void perform(load)}
      >
        {t('Preview channel identity')}
      </Button>
      {value && (
        <>
          {!value.multi_key && (
            <div className='grid gap-3 sm:grid-cols-2'>
              <label className='space-y-1 text-sm'>
                {t('Installation ID (empty uses a stable generated value)')}
                <Input
                  value={value.installation_id}
                  disabled={busy}
                  onChange={(event) =>
                    setValue({ ...value, installation_id: event.target.value })
                  }
                />
              </label>
              <label className='space-y-1 text-sm'>
                {t('Device ID (empty uses a stable generated value)')}
                <Input
                  value={value.device_id}
                  disabled={busy}
                  onChange={(event) =>
                    setValue({ ...value, device_id: event.target.value })
                  }
                />
              </label>
            </div>
          )}
          <div className='max-h-48 space-y-2 overflow-auto text-xs'>
            {value.slots.map((slot) => (
              <div
                key={slot.slot_id}
                className='bg-muted rounded p-2 break-all'
              >
                <div>
                  {t('Credential slot')}: {slot.slot_id}
                </div>
                <div>
                  {t('Installation ID')}: {slot.installation_id}
                </div>
                <div>
                  {t('Device ID')}: {slot.device_id}
                </div>
              </div>
            ))}
          </div>
          <div className='flex flex-wrap gap-2'>
            <Button
              type='button'
              disabled={busy}
              onClick={() => void perform(() => save(false))}
            >
              {t('Save channel identity')}
            </Button>
            <Button
              type='button'
              variant='outline'
              disabled={busy}
              onClick={() => void perform(() => save(true))}
            >
              {t('Reset generated identity and start new upstream sessions')}
            </Button>
          </div>
        </>
      )}
    </div>
  )
}
