<script setup lang="ts">
import { ClDatabaseOrmSettings } from '@/components';

defineOptions({ name: 'ClDatabaseDetailTabOverview' });
</script>

<template>
  <div class="database-detail-tab-overview">
    <cl-database-form readonly />
    <cl-database-orm-settings />
  </div>
</template>

<style scoped>
.database-detail-tab-overview {
  margin: 20px;
}
</style>
