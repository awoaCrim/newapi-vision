/*
Copyright (C) 2023-2026 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as
published by the Free Software Foundation, either version 3 of the
License, or (at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program. If not, see <https://www.gnu.org/licenses/>.

For commercial licensing, please contact support@quantumnous.com
*/
import { useState, useEffect } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import { KeyRound, Brain } from 'lucide-react'
import {
  Card,
  CardContent,
  CardHeader,
  CardTitle,
  CardDescription,
} from '@/components/ui/card'
import { Label } from '@/components/ui/label'
import { Input } from '@/components/ui/input'
import { Switch } from '@/components/ui/switch'
import { Button } from '@/components/ui/button'
import {
  Tooltip,
  TooltipContent,
  TooltipProvider,
  TooltipTrigger,
} from '@/components/ui/tooltip'
import { updateUserSettings } from '../api'
import { parseUserSettings } from '../lib'
import type { UserProfile } from '../types'

interface VisionSettingsCardProps {
  profile: UserProfile | null
  onSettingsUpdate: () => void
}

const DEFAULT_VISION = {
  enabled: false,
  vision_model_name: 'gpt-4o',
  prompt_template:
    'Please describe this image in detail, including all objects, text, people, colors, layout, and atmosphere.',
  vision_suffix: '-vision',
  phash_threshold: 10,
}

export function VisionSettingsCard({
  profile,
  onSettingsUpdate,
}: VisionSettingsCardProps) {
  const { t } = useTranslation()

  const existingSettings = parseUserSettings(profile?.setting)
  const vision = existingSettings.vision ?? DEFAULT_VISION

  const [enabled, setEnabled] = useState(vision.enabled)
  const [modelName, setModelName] = useState(vision.vision_model_name)
  const [promptTemplate, setPromptTemplate] = useState(vision.prompt_template)
  const [suffix, setSuffix] = useState(vision.vision_suffix)
  const [phashThreshold, setPhashThreshold] = useState(vision.phash_threshold)
  const [saving, setSaving] = useState(false)

  useEffect(() => {
    const settings = parseUserSettings(profile?.setting)
    const v = settings.vision
    if (v) {
      setEnabled(v.enabled)
      setModelName(v.vision_model_name)
      setPromptTemplate(v.prompt_template)
      setSuffix(v.vision_suffix)
      setPhashThreshold(v.phash_threshold)
    }
  }, [profile?.setting])

  const handleSave = async () => {
    setSaving(true)
    try {
      // Merge with existing settings so notify_type and other required fields are preserved
      const merged = parseUserSettings(profile?.setting)
      merged.vision = {
        enabled,
        vision_model_name: modelName,
        prompt_template: promptTemplate,
        vision_suffix: suffix,
        phash_threshold: phashThreshold,
      }
      const res = await updateUserSettings(merged)
      if (res.success) {
        toast.success(t('Vision settings saved'))
        onSettingsUpdate()
      } else {
        toast.error(res.message || t('Failed to save vision settings'))
      }
    } catch {
      toast.error(t('Failed to save vision settings'))
    } finally {
      setSaving(false)
    }
  }

  return (
    <Card>
      <CardHeader>
        <div className='flex items-center gap-2'>
          <Brain className='size-5 text-muted-foreground' />
          <div>
            <CardTitle>{t('Vision Interception')}</CardTitle>
            <CardDescription>
              {t(
                'Configure the vision model to automatically describe images as text for non-vision models. The model must be available in the system marketplace; usage will be billed to your account.'
              )}
            </CardDescription>
          </div>
        </div>
      </CardHeader>
      <CardContent className='space-y-4'>
        {/* Enable/Disable */}
        <div className='flex items-center justify-between'>
          <Label htmlFor='vision-enabled'>{t('Enable Vision')}</Label>
          <Switch
            id='vision-enabled'
            checked={enabled}
            onCheckedChange={setEnabled}
          />
        </div>

        {enabled && (
          <>
            {/* Vision Model Name */}
            <div className='space-y-2'>
              <Label htmlFor='vision-model'>{t('Vision Model')}</Label>
              <Input
                id='vision-model'
                placeholder='gpt-4o'
                value={modelName}
                onChange={(e) => setModelName(e.target.value)}
              />
              <p className='text-xs text-muted-foreground'>
                {t(
                  'Model name must match an existing model in the system marketplace. The channel credentials will be used automatically.'
                )}
              </p>
            </div>

            {/* Prompt Template */}
            <div className='space-y-2'>
              <Label htmlFor='vision-prompt'>{t('Prompt Template')}</Label>
              <textarea
                id='vision-prompt'
                className='flex min-h-[80px] w-full rounded-md border border-input bg-background px-3 py-2 text-sm ring-offset-background placeholder:text-muted-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2 disabled:cursor-not-allowed disabled:opacity-50'
                placeholder='Please describe this image...'
                value={promptTemplate}
                onChange={(e) => setPromptTemplate(e.target.value)}
              />
            </div>

            {/* Suffix */}
            <div className='space-y-2'>
              <div className='flex items-center gap-1.5'>
                <Label htmlFor='vision-suffix'>{t('Model Suffix')}</Label>
                <TooltipProvider>
                  <Tooltip>
                    <TooltipTrigger asChild>
                      <KeyRound className='size-3.5 text-muted-foreground' />
                    </TooltipTrigger>
                    <TooltipContent>
                      {t(
                        'Append this suffix to model names to trigger vision interception.'
                      )}
                    </TooltipContent>
                  </Tooltip>
                </TooltipProvider>
              </div>
              <Input
                id='vision-suffix'
                placeholder='-vision'
                value={suffix}
                onChange={(e) => setSuffix(e.target.value)}
              />
            </div>

            {/* pHash Threshold */}
            <div className='space-y-2'>
              <Label htmlFor='vision-phash'>{t('pHash Threshold')}</Label>
              <Input
                id='vision-phash'
                type='number'
                min={0}
                max={64}
                placeholder='10'
                value={phashThreshold}
                onChange={(e) => setPhashThreshold(parseInt(e.target.value) || 0)}
              />
              <p className='text-xs text-muted-foreground'>
                {t(
                  'Hamming distance threshold for grouping similar images. 0 = disabled.'
                )}
              </p>
            </div>
          </>
        )}

        {/* Save Button */}
        <div className='flex justify-end'>
          <Button onClick={handleSave} disabled={saving}>
            {saving ? t('Saving...') : t('Save Vision Settings')}
          </Button>
        </div>
      </CardContent>
    </Card>
  )
}
